package relay

import (
	"reflect"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// preserveVideoRequestExtras carries unknown non-media parameters through the
// same-protocol typed adaptor path. Known fields remain governed by that adaptor.
func preserveVideoRequestExtras(c *gin.Context, info *relaycommon.RelayInfo, converted []byte) ([]byte, error) {
	if !c.GetBool("video_compatibility_deferred") || info.Request == nil || info.GetFinalRequestRelayFormat() != info.RelayFormat {
		return converted, nil
	}
	storage, err := common.GetBodyStorage(c)
	if err != nil {
		return nil, err
	}
	raw, err := storage.Bytes()
	if err != nil {
		return nil, err
	}
	schema := reflect.TypeOf(info.Request)
	if strings.HasPrefix(c.Request.URL.Path, "/pg/chat/completions") {
		// Playground consumes group in the host; it is not a provider extension.
		schema = reflect.TypeFor[struct {
			*dto.GeneralOpenAIRequest
			Group string `json:"group,omitempty"`
		}]()
	}
	return restoreVideoRequestUnknownFields(converted, gjson.ParseBytes(raw), gjson.ParseBytes(converted), schema, "", 0)
}

func videoRequestJSONFields(schema reflect.Type) map[string]reflect.Type {
	for schema.Kind() == reflect.Pointer {
		schema = schema.Elem()
	}
	if schema.Kind() != reflect.Struct {
		return nil
	}
	fields := make(map[string]reflect.Type)
	for i := range schema.NumField() {
		field := schema.Field(i)
		if field.PkgPath != "" {
			continue
		}
		name, _, _ := strings.Cut(field.Tag.Get("json"), ",")
		if name == "-" {
			continue
		}
		if name == "" && field.Anonymous {
			for key, nested := range videoRequestJSONFields(field.Type) {
				fields[key] = nested
			}
			continue
		}
		if name == "" {
			name = field.Name
		}
		// The request decoder also accepts case-insensitive known field names.
		fields[strings.ToLower(name)] = field.Type
	}
	return fields
}

func restoreVideoRequestUnknownFields(converted []byte, source, target gjson.Result, schema reflect.Type, path string, depth int) ([]byte, error) {
	// Untyped/raw fields already retain their JSON. Do not undo their explicit
	// adaptor normalization, and bound recursion independently of request size.
	if depth > 32 {
		return converted, nil
	}
	for schema.Kind() == reflect.Pointer {
		schema = schema.Elem()
	}
	if schema.Kind() == reflect.Slice || schema.Kind() == reflect.Array {
		if schema.Elem().Kind() == reflect.Uint8 {
			return converted, nil
		}
		if !source.IsArray() || !target.IsArray() {
			return converted, nil
		}
		sourceItems, targetItems := source.Array(), target.Array()
		if len(sourceItems) != len(targetItems) {
			return converted, nil
		}
		var err error
		for i := range sourceItems {
			converted, err = restoreVideoRequestUnknownFields(converted, sourceItems[i], targetItems[i], schema.Elem(), path+"."+strconv.Itoa(i), depth+1)
			if err != nil {
				return nil, err
			}
		}
		return converted, nil
	}
	if schema.Kind() != reflect.Struct || !source.IsObject() || !target.IsObject() {
		return converted, nil
	}
	// A conversion may replace/reorder a message or tool while retaining the
	// same array length. Metadata belongs only to nodes with the same identity.
	fields := videoRequestJSONFields(schema)
	for _, key := range []string{"type", "role", "id", "name", "function.name", "tool_call_id", "tool_use_id"} {
		fieldName, _, _ := strings.Cut(key, ".")
		if _, known := fields[fieldName]; !known {
			continue
		}
		original, current := source.Get(key), target.Get(key)
		if original.Exists() != current.Exists() || original.Exists() && original.String() != current.String() {
			return converted, nil
		}
	}
	targetFields := target.Map()
	var err error
	source.ForEach(func(key, value gjson.Result) bool {
		fieldName := key.Str
		current, exists := targetFields[fieldName]
		fieldType, known := fields[strings.ToLower(fieldName)]
		if known && !exists {
			for name, item := range targetFields {
				if strings.EqualFold(name, fieldName) {
					fieldName, current, exists = name, item, true
					break
				}
			}
		}
		fieldPath := strings.NewReplacer("\\", "\\\\", ".", "\\.", ":", "\\:").Replace(fieldName)
		if path != "" {
			fieldPath = path + "." + fieldPath
		}
		if !known {
			if !exists {
				converted, err = sjson.SetRawBytes(converted, fieldPath, []byte(value.Raw))
			}
		} else if exists {
			converted, err = restoreVideoRequestUnknownFields(converted, value, current, fieldType, fieldPath, depth+1)
		}
		return err == nil
	})
	return converted, err
}
