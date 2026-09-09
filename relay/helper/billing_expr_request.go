package helper

import (
	"fmt"
	"io"
	"maps"
	"mime"
	"mime/multipart"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/billingexpr"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/gin-gonic/gin"
)

const billingMultipartInputKey = "_billing_expr_multipart_input"

func ResolveIncomingBillingExprRequestInput(c *gin.Context, info *relaycommon.RelayInfo) (billingexpr.RequestInput, error) {
	if info != nil && info.BillingRequestInput != nil {
		input := cloneRequestInput(*info.BillingRequestInput)
		if input.Multipart == nil && isMultipartContentType(c) {
			multipartInput, err := readIncomingBillingExprMultipart(c)
			if err != nil {
				return billingexpr.RequestInput{}, err
			}
			input.Multipart = multipartInput
		}
		merged := cloneStringMap(info.RequestHeaders)
		maps.Copy(merged, input.Headers)
		input.Headers = merged
		return input, nil
	}

	input := billingexpr.RequestInput{}
	if info != nil {
		input.Headers = cloneStringMap(info.RequestHeaders)
	}

	bodyBytes, err := readIncomingBillingExprBody(c)
	if err != nil {
		return billingexpr.RequestInput{}, err
	}
	input.Body = bodyBytes
	if isMultipartContentType(c) {
		multipartInput, err := readIncomingBillingExprMultipart(c)
		if err != nil {
			return billingexpr.RequestInput{}, err
		}
		input.Multipart = multipartInput
	}
	return input, nil
}

func BuildBillingExprRequestInputFromRequest(request dto.Request, headers map[string]string) (billingexpr.RequestInput, error) {
	input := billingexpr.RequestInput{
		Headers: cloneStringMap(headers),
	}
	if request == nil {
		return input, nil
	}

	bodyBytes, err := common.Marshal(request)
	if err != nil {
		return billingexpr.RequestInput{}, err
	}
	input.Body = bodyBytes
	return input, nil
}

func readIncomingBillingExprBody(c *gin.Context) ([]byte, error) {
	if c == nil || c.Request == nil || !isJSONContentType(c.Request.Header.Get("Content-Type")) {
		return nil, nil
	}
	storage, err := common.GetBodyStorage(c)
	if err != nil {
		return nil, err
	}
	return storage.Bytes()
}

func readIncomingBillingExprMultipart(c *gin.Context) (*billingexpr.MultipartInput, error) {
	if c == nil || c.Request == nil {
		return nil, nil
	}
	if cached, ok := c.Get(billingMultipartInputKey); ok {
		if input, ok := cached.(*billingexpr.MultipartInput); ok {
			return input, nil
		}
	}

	storage, err := common.GetBodyStorage(c)
	if err != nil {
		return nil, err
	}
	if _, err := storage.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	// GetBodyStorage may have consumed the original request body while creating
	// the replayable storage. Restore it before returning so downstream request
	// validation and adapters see the original multipart stream.
	c.Request.Body = io.NopCloser(storage)
	reader, err := storage.NewReader()
	if err != nil {
		return nil, err
	}
	defer reader.Close()

	_, params, err := mime.ParseMediaType(incomingContentType(c))
	if err != nil {
		return nil, err
	}
	boundary := params["boundary"]
	if boundary == "" {
		return nil, fmt.Errorf("multipart boundary is missing")
	}

	multipartReader := multipart.NewReader(reader, boundary)

	input := &billingexpr.MultipartInput{
		Fields: make(map[string][]string),
		Files:  make(map[string][]billingexpr.MultipartFileMetadata),
	}
	for {
		part, nextErr := multipartReader.NextPart()
		if nextErr == io.EOF {
			break
		}
		if nextErr != nil {
			return nil, nextErr
		}

		name := part.FormName()
		if name == "" {
			continue
		}
		if part.FileName() == "" {
			value, readErr := io.ReadAll(part)
			if readErr != nil {
				return nil, readErr
			}
			input.Fields[name] = append(input.Fields[name], string(value))
			continue
		}

		// Only inspect multipart headers. Do not open or copy uploaded file
		// content; NextPart discards the unread part while advancing.
		size := int64(0)
		if contentLength := strings.TrimSpace(part.Header.Get("Content-Length")); contentLength != "" {
			if parsed, parseErr := strconv.ParseInt(contentLength, 10, 64); parseErr == nil && parsed >= 0 {
				size = parsed
			}
		}
		input.Files[name] = append(input.Files[name], billingexpr.MultipartFileMetadata{
			Filename:    part.FileName(),
			ContentType: part.Header.Get("Content-Type"),
			Size:        size,
		})
	}
	if len(input.Fields) == 0 && len(input.Files) == 0 {
		return nil, nil
	}
	c.Set(billingMultipartInputKey, input)
	return input, nil
}

func cloneRequestInput(src billingexpr.RequestInput) billingexpr.RequestInput {
	input := billingexpr.RequestInput{
		Headers: cloneStringMap(src.Headers),
	}
	if len(src.Body) > 0 {
		input.Body = append([]byte(nil), src.Body...)
	}
	if src.Multipart != nil {
		input.Multipart = &billingexpr.MultipartInput{
			Fields: make(map[string][]string, len(src.Multipart.Fields)),
			Files:  make(map[string][]billingexpr.MultipartFileMetadata, len(src.Multipart.Files)),
		}
		for key, values := range src.Multipart.Fields {
			input.Multipart.Fields[key] = append([]string(nil), values...)
		}
		for key, files := range src.Multipart.Files {
			input.Multipart.Files[key] = append([]billingexpr.MultipartFileMetadata(nil), files...)
		}
	}
	return input
}

func isJSONContentType(contentType string) bool {
	contentType = strings.ToLower(strings.TrimSpace(contentType))
	return strings.HasPrefix(contentType, "application/json")
}

func isMultipartContentType(c *gin.Context) bool {
	if c == nil || c.Request == nil {
		return false
	}
	contentType := strings.ToLower(strings.TrimSpace(incomingContentType(c)))
	return strings.HasPrefix(contentType, "multipart/form-data")
}

func incomingContentType(c *gin.Context) string {
	if c == nil || c.Request == nil {
		return ""
	}
	if saved, ok := c.Get("_original_multipart_ct"); ok {
		if contentType, ok := saved.(string); ok && strings.TrimSpace(contentType) != "" {
			return contentType
		}
	}
	return c.Request.Header.Get("Content-Type")
}

func cloneStringMap(src map[string]string) map[string]string {
	if len(src) == 0 {
		return map[string]string{}
	}
	dst := make(map[string]string, len(src))
	for key, value := range src {
		if strings.TrimSpace(key) == "" {
			continue
		}
		dst[key] = value
	}
	return dst
}
