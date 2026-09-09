package billingexpr

import (
	"fmt"
	"math"
	"strings"
	"sync"

	"github.com/expr-lang/expr"
	"github.com/expr-lang/expr/ast"
	"github.com/expr-lang/expr/vm"
)

const maxCacheSize = 256

// DefaultExprVersion is used when an expression string has no version prefix.
const DefaultExprVersion = 1

const (
	requestRuleTraceFunction    = "_trace"
	requestRuleTraceIntFunction = "_trace_int"
)

// ParseExprVersion extracts the version tag and body from an expression string.
// Format: "v1:tier(...)" → version=1, body="tier(...)".
// No prefix defaults to DefaultExprVersion.
func ParseExprVersion(exprStr string) (version int, body string) {
	if strings.HasPrefix(exprStr, "v1:") {
		return 1, exprStr[3:]
	}
	return DefaultExprVersion, exprStr
}

// requestRulePatcher adds trace side effects to existing request multipliers
// without changing the stored expression or its numeric result.
type requestRulePatcher struct {
	requestRules         []RequestRuleTrace
	restrictedIdentifier string
}

func (p *requestRulePatcher) Visit(node *ast.Node) {
	if identifier, ok := (*node).(*ast.IdentifierNode); ok {
		switch identifier.Value {
		case requestRuleTraceFunction, requestRuleTraceIntFunction:
			p.restrictedIdentifier = identifier.Value
		}
		return
	}

	conditional, ok := (*node).(*ast.ConditionalNode)
	if !ok || !conditional.Ternary || !usesRequestProbe(conditional.Cond) {
		return
	}
	multiplier, ok := requestRuleNumber(conditional.Exp1)
	fallback, fallbackOK := requestRuleNumber(conditional.Exp2)
	if !ok || !fallbackOK || fallback != 1 {
		return
	}

	ruleIndex := len(p.requestRules)
	p.requestRules = append(p.requestRules, RequestRuleTrace{
		Cond:       conditional.Cond.String(),
		Multiplier: multiplier,
	})

	traceFunction := requestRuleTraceFunction
	var multiplierNode ast.Node = &ast.FloatNode{Value: multiplier}
	if _, multiplierIsInt := conditional.Exp1.(*ast.IntegerNode); multiplierIsInt {
		if _, fallbackIsInt := conditional.Exp2.(*ast.IntegerNode); fallbackIsInt {
			traceFunction = requestRuleTraceIntFunction
			multiplierNode = conditional.Exp1
		}
	}

	ast.Patch(node, &ast.CallNode{
		Callee: &ast.IdentifierNode{Value: traceFunction},
		Arguments: []ast.Node{
			&ast.IntegerNode{Value: ruleIndex},
			conditional.Cond,
			multiplierNode,
		},
	})
}

func requestRuleNumber(node ast.Node) (float64, bool) {
	switch value := node.(type) {
	case *ast.IntegerNode:
		return float64(value.Value), true
	case *ast.FloatNode:
		return value.Value, true
	default:
		return 0, false
	}
}

func usesRequestProbe(node ast.Node) bool {
	return ast.Find(node, func(node ast.Node) bool {
		identifier, ok := node.(*ast.IdentifierNode)
		if !ok {
			return false
		}
		switch identifier.Value {
		case "param", "header", "hour", "minute", "weekday", "month", "day":
			return true
		default:
			return false
		}
	}) != nil
}

type cachedEntry struct {
	prog          *vm.Program
	usedVars      map[string]bool
	usedUsageKeys map[string]bool
	requestRules  []RequestRuleTrace
	version       int
}

var (
	cacheMu sync.RWMutex
	cache   = make(map[string]*cachedEntry, 64)
)

// compileEnvPrototypeV1 is the v1 type-checking prototype used at compile time.
var compileEnvPrototypeV1 = map[string]any{
	"p":                  float64(0),
	"c":                  float64(0),
	"p_total":            float64(0),
	"c_total":            float64(0),
	"cr_total":           float64(0),
	"cc_total":           float64(0),
	"cc1h_total":         float64(0),
	"img_total":          float64(0),
	"img_o_total":        float64(0),
	"ai_total":           float64(0),
	"ao_total":           float64(0),
	"len":                float64(0),
	"cr":                 float64(0),
	"cc":                 float64(0),
	"cc1h":               float64(0),
	"img":                float64(0),
	"img_o":              float64(0),
	"ai":                 float64(0),
	"ao":                 float64(0),
	"tier":               func(string, float64) float64 { return 0 },
	"header":             func(string) string { return "" },
	"param":              func(string) any { return nil },
	"multipart_param":    func(string) any { return nil },
	"has":                func(any, string) bool { return false },
	"rule_override":      func(float64, bool, float64) float64 { return 0 },
	"rule_override_tier": func(float64, bool, float64, string) float64 { return 0 },
	"hour":               func(string) int { return 0 },
	"minute":             func(string) int { return 0 },
	"weekday":            func(string) int { return 0 },
	"month":              func(string) int { return 0 },
	"day":                func(string) int { return 0 },
	"max":                math.Max,
	"min":                math.Min,
	"abs":                math.Abs,
	"ceil":               math.Ceil,
	"floor":              math.Floor,
	"_trace":             func(int, bool, float64) float64 { return 1 },
	"_trace_int":         func(int, bool, int) int { return 1 },
	"u":                  func(string) any { return nil },
}

func getCompileEnv(version int) map[string]any {
	switch version {
	default:
		return compileEnvPrototypeV1
	}
}

// CompileFromCache compiles an expression string, using a cached program when
// available. The cache is keyed by the SHA-256 hex digest of the expression.
func CompileFromCache(exprStr string) (*vm.Program, error) {
	return compileFromCacheByHash(exprStr, ExprHashString(exprStr))
}

// CompileFromCacheByHash is like CompileFromCache but accepts a pre-computed
// hash, useful when the caller already has the BillingSnapshot.ExprHash.
func CompileFromCacheByHash(exprStr, hash string) (*vm.Program, error) {
	return compileFromCacheByHash(exprStr, hash)
}

func compileFromCacheByHash(exprStr, hash string) (*vm.Program, error) {
	entry, err := compileEntryFromCacheByHash(exprStr, hash)
	if err != nil {
		return nil, err
	}
	return entry.prog, nil
}

func compileEntryFromCacheByHash(exprStr, hash string) (*cachedEntry, error) {
	cacheMu.RLock()
	if entry, ok := cache[hash]; ok {
		cacheMu.RUnlock()
		return entry, nil
	}
	cacheMu.RUnlock()

	version, body := ParseExprVersion(exprStr)
	body = canonicalizeDirectOverrideExpr(body)
	patcher := &requestRulePatcher{}
	prog, err := expr.Compile(body, expr.Env(getCompileEnv(version)), expr.Patch(patcher), expr.AsFloat64())
	if patcher.restrictedIdentifier != "" {
		return nil, fmt.Errorf("expr compile error: identifier %q is reserved for internal use", patcher.restrictedIdentifier)
	}
	if err != nil {
		return nil, fmt.Errorf("expr compile error: %w", err)
	}

	entry := &cachedEntry{
		prog:          prog,
		usedVars:      extractUsedVars(prog),
		usedUsageKeys: extractUsedUsageKeys(prog),
		requestRules:  patcher.requestRules,
		version:       version,
	}
	cacheMu.Lock()
	if len(cache) >= maxCacheSize {
		cache = make(map[string]*cachedEntry, 64)
	}
	cache[hash] = entry
	cacheMu.Unlock()

	return entry, nil
}

// ExprVersion returns the version of a cached expression. Returns DefaultExprVersion
// if the expression hasn't been compiled yet or is empty.
func ExprVersion(exprStr string) int {
	if exprStr == "" {
		return DefaultExprVersion
	}
	hash := ExprHashString(exprStr)
	cacheMu.RLock()
	if entry, ok := cache[hash]; ok {
		cacheMu.RUnlock()
		return entry.version
	}
	cacheMu.RUnlock()
	v, _ := ParseExprVersion(exprStr)
	return v
}

func extractUsedVars(prog *vm.Program) map[string]bool {
	vars := make(map[string]bool)
	node := prog.Node()
	ast.Find(node, func(n ast.Node) bool {
		if id, ok := n.(*ast.IdentifierNode); ok {
			switch id.Value {
			case requestRuleTraceFunction, requestRuleTraceIntFunction:
				return false
			}
			vars[id.Value] = true
		}
		return false
	})
	return vars
}

func extractUsedUsageKeys(prog *vm.Program) map[string]bool {
	keys := make(map[string]bool)
	ast.Find(prog.Node(), func(node ast.Node) bool {
		call, ok := node.(*ast.CallNode)
		if !ok || len(call.Arguments) != 1 {
			return false
		}
		callee, ok := call.Callee.(*ast.IdentifierNode)
		if !ok || callee.Value != "u" {
			return false
		}
		literal, ok := call.Arguments[0].(*ast.StringNode)
		if !ok {
			return false
		}
		keys[strings.TrimSpace(literal.Value)] = true
		return false
	})
	return keys
}

// UsedVars returns the set of identifier names referenced by an expression.
// The result is cached alongside the compiled program. Returns nil for empty input.
func UsedVars(exprStr string) map[string]bool {
	if exprStr == "" {
		return nil
	}
	hash := ExprHashString(exprStr)
	cacheMu.RLock()
	if entry, ok := cache[hash]; ok {
		cacheMu.RUnlock()
		return entry.usedVars
	}
	cacheMu.RUnlock()

	// Compile (and cache) to populate usedVars
	if _, err := compileFromCacheByHash(exprStr, hash); err != nil {
		return nil
	}
	cacheMu.RLock()
	entry, ok := cache[hash]
	cacheMu.RUnlock()
	if ok {
		return entry.usedVars
	}
	return nil
}

// UsedUsageKeys returns literal keys referenced by u("...") calls. Calls with
// dynamic arguments are intentionally omitted because they cannot be
// validated statically.
func UsedUsageKeys(exprStr string) map[string]bool {
	if exprStr == "" {
		return nil
	}
	hash := ExprHashString(exprStr)
	cacheMu.RLock()
	if entry, ok := cache[hash]; ok {
		cacheMu.RUnlock()
		return entry.usedUsageKeys
	}
	cacheMu.RUnlock()

	if _, err := compileFromCacheByHash(exprStr, hash); err != nil {
		return nil
	}
	cacheMu.RLock()
	entry, ok := cache[hash]
	cacheMu.RUnlock()
	if ok {
		return entry.usedUsageKeys
	}
	return nil
}

// InvalidateCache clears the compiled-expression cache.
// Called when billing rules are updated.
func InvalidateCache() {
	cacheMu.Lock()
	cache = make(map[string]*cachedEntry, 64)
	cacheMu.Unlock()
}

var directOverrideRawVars = map[string]string{
	"cr":    "cr_total",
	"cc":    "cc_total",
	"cc1h":  "cc1h_total",
	"img":   "img_total",
	"ai":    "ai_total",
	"img_o": "img_o_total",
	"ao":    "ao_total",
}

// canonicalizeDirectOverrideExpr keeps direct overrides independent from the
// base expression's p/c remainder. Older expressions may use p/c/cr directly
// in the override cost; rewrite those identifiers to raw totals before
// compilation while preserving quoted strings and nested function calls.
func canonicalizeDirectOverrideExpr(exprStr string) string {
	var out strings.Builder
	for i := 0; i < len(exprStr); {
		if exprStr[i] == '"' || exprStr[i] == '\'' {
			end := scanQuoted(exprStr, i)
			out.WriteString(exprStr[i:end])
			i = end
			continue
		}

		name, ok := directOverrideFunctionAt(exprStr, i)
		if !ok {
			out.WriteByte(exprStr[i])
			i++
			continue
		}
		open := i + len(name)
		close, ok := matchingParen(exprStr, open)
		if !ok {
			out.WriteByte(exprStr[i])
			i++
			continue
		}
		args, ok := splitExpressionArgs(exprStr[open+1 : close])
		wantArgs := 3
		if name == "rule_override_tier" {
			wantArgs = 4
		}
		if len(args) != wantArgs {
			out.WriteString(exprStr[i : close+1])
			i = close + 1
			continue
		}

		args[0] = canonicalizeDirectOverrideExpr(args[0])
		args[1] = canonicalizeDirectOverrideExpr(args[1])
		costExpr := args[2]
		if name == "rule_override_tier" {
			costExpr = unwrapDirectOverrideTier(costExpr)
		}
		args[2] = canonicalizeDirectOverrideCost(costExpr)
		out.WriteString(name)
		out.WriteByte('(')
		out.WriteString(strings.Join(args, ", "))
		out.WriteByte(')')
		i = close + 1
	}
	return out.String()
}

// unwrapDirectOverrideTier removes the legacy tier(name, value) label wrapper
// from a rule_override_tier override argument. The enclosing function already
// carries the authoritative override name; removing the eager tier callback
// keeps an unmatched override from leaking its trace into the base tier.
func unwrapDirectOverrideTier(exprStr string) string {
	trimmed := strings.TrimSpace(exprStr)
	if !strings.HasPrefix(trimmed, "tier(") {
		return exprStr
	}
	close, ok := matchingParen(trimmed, len("tier"))
	if !ok || close != len(trimmed)-1 {
		return exprStr
	}
	args, ok := splitExpressionArgs(trimmed[len("tier("):close])
	if !ok || len(args) != 2 {
		return exprStr
	}
	return args[1]
}

func canonicalizeDirectOverrideCost(exprStr string) string {
	promptExtras := make(map[string]bool)
	outputExtras := make(map[string]bool)
	for _, token := range expressionIdentifiers(exprStr) {
		switch token {
		case "cr", "cc", "cc1h", "img", "ai":
			promptExtras[token] = true
		case "img_o", "ao":
			outputExtras[token] = true
		}
	}
	promptRemainder := "p_total"
	for _, token := range []string{"cr", "cc", "cc1h", "img", "ai"} {
		if promptExtras[token] {
			promptRemainder += " - " + directOverrideRawVars[token]
		}
	}
	outputRemainder := "c_total"
	for _, token := range []string{"img_o", "ao"} {
		if outputExtras[token] {
			outputRemainder += " - " + directOverrideRawVars[token]
		}
	}

	return rewriteExpressionIdentifiers(exprStr, func(token string) string {
		switch token {
		case "p":
			return "(" + promptRemainder + ")"
		case "c":
			return "(" + outputRemainder + ")"
		default:
			if raw, ok := directOverrideRawVars[token]; ok {
				return raw
			}
			return token
		}
	})
}

func expressionIdentifiers(exprStr string) []string {
	var identifiers []string
	for i := 0; i < len(exprStr); {
		if exprStr[i] == '"' || exprStr[i] == '\'' {
			i = scanQuoted(exprStr, i)
			continue
		}
		if !isIdentifierStart(exprStr[i]) {
			i++
			continue
		}
		start := i
		i++
		for i < len(exprStr) && isIdentifierChar(exprStr[i]) {
			i++
		}
		identifiers = append(identifiers, exprStr[start:i])
	}
	return identifiers
}

func rewriteExpressionIdentifiers(exprStr string, rewrite func(string) string) string {
	var out strings.Builder
	for i := 0; i < len(exprStr); {
		if exprStr[i] == '"' || exprStr[i] == '\'' {
			end := scanQuoted(exprStr, i)
			out.WriteString(exprStr[i:end])
			i = end
			continue
		}
		if !isIdentifierStart(exprStr[i]) {
			out.WriteByte(exprStr[i])
			i++
			continue
		}
		start := i
		i++
		for i < len(exprStr) && isIdentifierChar(exprStr[i]) {
			i++
		}
		out.WriteString(rewrite(exprStr[start:i]))
	}
	return out.String()
}

func directOverrideFunctionAt(exprStr string, offset int) (string, bool) {
	for _, name := range []string{"rule_override_tier", "rule_override"} {
		if !strings.HasPrefix(exprStr[offset:], name) {
			continue
		}
		beforeOK := offset == 0 || !isIdentifierChar(exprStr[offset-1])
		end := offset + len(name)
		if beforeOK && end < len(exprStr) && exprStr[end] == '(' {
			return name, true
		}
	}
	return "", false
}

func matchingParen(exprStr string, open int) (int, bool) {
	depth := 0
	for i := open; i < len(exprStr); i++ {
		switch exprStr[i] {
		case '"', '\'':
			i = scanQuoted(exprStr, i) - 1
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return i, true
			}
		}
	}
	return 0, false
}

func splitExpressionArgs(exprStr string) ([]string, bool) {
	args := make([]string, 0, 4)
	start := 0
	depth := 0
	for i := 0; i < len(exprStr); i++ {
		switch exprStr[i] {
		case '"', '\'':
			i = scanQuoted(exprStr, i) - 1
		case '(':
			depth++
		case ')':
			depth--
		case ',':
			if depth == 0 {
				args = append(args, strings.TrimSpace(exprStr[start:i]))
				start = i + 1
			}
		}
	}
	if depth != 0 {
		return nil, false
	}
	args = append(args, strings.TrimSpace(exprStr[start:]))
	return args, true
}

func scanQuoted(exprStr string, start int) int {
	quote := exprStr[start]
	for i := start + 1; i < len(exprStr); i++ {
		if exprStr[i] == '\\' {
			i++
			continue
		}
		if exprStr[i] == quote {
			return i + 1
		}
	}
	return len(exprStr)
}

func isIdentifierStart(char byte) bool {
	return (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') || char == '_'
}

func isIdentifierChar(char byte) bool {
	return isIdentifierStart(char) || (char >= '0' && char <= '9')
}
