package billingexpr

import (
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/expr-lang/expr"
	"github.com/expr-lang/expr/vm"
	"github.com/tidwall/gjson"
)

// RunExpr compiles (with cache) and executes an expression string.
// The environment exposes:
//   - p, c             — prompt / completion tokens (auto-excluding separately-priced sub-categories)
//   - len              — total input context length for tier conditions (never reduced by sub-category exclusion)
//   - cr, cc, cc1h     — cache read / creation / creation-1h tokens
//   - tier(name, value) — trace callback that records which tier matched
//   - multipart_param(path) — reads explicit multipart metadata
//   - rule_override_tier(base, matched, override, name) — direct request-rule price override
//   - max, min, abs, ceil, floor — standard math helpers
//
// Returns the resulting float64 quota (before group ratio) and a TraceResult
// with side-channel info captured by tier() during execution.
func RunExpr(exprStr string, params TokenParams) (float64, TraceResult, error) {
	return RunExprWithRequest(exprStr, params, RequestInput{})
}

func RunExprWithRequest(exprStr string, params TokenParams, request RequestInput) (float64, TraceResult, error) {
	entry, err := compileEntryFromCacheByHash(exprStr, ExprHashString(exprStr))
	if err != nil {
		return 0, TraceResult{}, err
	}
	return runProgram(entry.prog, entry.requestRules, params, request)
}

// RunExprByHash is like RunExpr but accepts a pre-computed hash for the cache
// lookup, avoiding a redundant SHA-256 computation when the caller already
// holds BillingSnapshot.ExprHash.
func RunExprByHash(exprStr, hash string, params TokenParams) (float64, TraceResult, error) {
	return RunExprByHashWithRequest(exprStr, hash, params, RequestInput{})
}

func RunExprByHashWithRequest(exprStr, hash string, params TokenParams, request RequestInput) (float64, TraceResult, error) {
	entry, err := compileEntryFromCacheByHash(exprStr, hash)
	if err != nil {
		return 0, TraceResult{}, err
	}
	return runProgram(entry.prog, entry.requestRules, params, request)
}

func runProgram(prog *vm.Program, requestRules []RequestRuleTrace, params TokenParams, request RequestInput) (float64, TraceResult, error) {
	trace := TraceResult{
		RequestRules: append([]RequestRuleTrace(nil), requestRules...),
	}
	headers := normalizeHeaders(request.Headers)
	var multipartJSON []byte
	if request.Multipart != nil {
		var err error
		multipartJSON, err = common.Marshal(request.Multipart)
		if err != nil {
			return 0, trace, fmt.Errorf("multipart metadata encode error: %w", err)
		}
	}
	rawP := params.RawP
	if rawP == 0 && params.P != 0 {
		rawP = params.P
	}
	rawC := params.RawC
	if rawC == 0 && params.C != 0 {
		rawC = params.C
	}

	env := map[string]any{
		"p":           params.P,
		"c":           params.C,
		"p_total":     rawP,
		"c_total":     rawC,
		"cr_total":    params.CR,
		"cc_total":    params.CC,
		"cc1h_total":  params.CC1h,
		"img_total":   params.Img,
		"img_o_total": params.ImgO,
		"ai_total":    params.AI,
		"ao_total":    params.AO,
		"len":         params.Len,
		"cr":          params.CR,
		"cc":          params.CC,
		"cc1h":        params.CC1h,
		"img":         params.Img,
		"img_o":       params.ImgO,
		"ai":          params.AI,
		"ao":          params.AO,
		"tier": func(name string, value float64) float64 {
			trace.MatchedTier = name
			trace.Cost = value
			return value
		},
		requestRuleTraceFunction: func(ruleIndex int, matched bool, multiplier float64) float64 {
			if matched && ruleIndex >= 0 && ruleIndex < len(trace.RequestRules) {
				trace.RequestRules[ruleIndex].Matched = true
			}
			if matched {
				return multiplier
			}
			return 1
		},
		requestRuleTraceIntFunction: func(ruleIndex int, matched bool, multiplier int) int {
			if matched && ruleIndex >= 0 && ruleIndex < len(trace.RequestRules) {
				trace.RequestRules[ruleIndex].Matched = true
			}
			if matched {
				return multiplier
			}
			return 1
		},
		"header": func(key string) string {
			return headers[strings.ToLower(strings.TrimSpace(key))]
		},
		"param": func(path string) any {
			path = strings.TrimSpace(path)
			if path == "" || len(request.Body) == 0 {
				return nil
			}
			result := gjson.GetBytes(request.Body, path)
			if !result.Exists() {
				return nil
			}
			return result.Value()
		},
		"multipart_param": func(path string) any {
			path = strings.TrimSpace(path)
			if path == "" || len(multipartJSON) == 0 {
				return nil
			}
			result := gjson.GetBytes(multipartJSON, path)
			if !result.Exists() {
				return nil
			}
			return result.Value()
		},
		"rule_override": func(base float64, matched bool, override float64) float64 {
			if matched {
				return override
			}
			return base
		},
		"rule_override_tier": func(base float64, matched bool, override float64, name string) float64 {
			if matched {
				trace.MatchedTier = name
				trace.Cost = override
				return override
			}
			// The base argument is evaluated before this callback. Keep the trace
			// it produced, including a nested override that already matched.
			return base
		},
		"u": func(name string) any {
			if request.Usage == nil {
				return nil
			}
			return request.Usage[strings.TrimSpace(name)]
		},
		"has": func(source any, substr string) bool {
			if source == nil || substr == "" {
				return false
			}
			return strings.Contains(fmt.Sprint(source), substr)
		},
		"hour":    func(tz string) int { return timeInZone(tz).Hour() },
		"minute":  func(tz string) int { return timeInZone(tz).Minute() },
		"weekday": func(tz string) int { return int(timeInZone(tz).Weekday()) },
		"month":   func(tz string) int { return int(timeInZone(tz).Month()) },
		"day":     func(tz string) int { return timeInZone(tz).Day() },
		"max":     math.Max,
		"min":     math.Min,
		"abs":     math.Abs,
		"ceil":    math.Ceil,
		"floor":   math.Floor,
	}

	out, err := expr.Run(prog, env)
	if err != nil {
		return 0, trace, fmt.Errorf("expr run error: %w", err)
	}
	f, ok := out.(float64)
	if !ok {
		return 0, trace, fmt.Errorf("expr result is %T, want float64", out)
	}
	if f < 0 || math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, trace, fmt.Errorf("expr result must be finite and non-negative, got %g", f)
	}
	return f, trace, nil
}

func timeInZone(tz string) time.Time {
	tz = strings.TrimSpace(tz)
	if tz == "" {
		return time.Now().UTC()
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return time.Now().UTC()
	}
	return time.Now().In(loc)
}

func normalizeHeaders(headers map[string]string) map[string]string {
	if len(headers) == 0 {
		return map[string]string{}
	}
	normalized := make(map[string]string, len(headers))
	for key, value := range headers {
		k := strings.ToLower(strings.TrimSpace(key))
		v := strings.TrimSpace(value)
		if k == "" || v == "" {
			continue
		}
		normalized[k] = v
	}
	return normalized
}
