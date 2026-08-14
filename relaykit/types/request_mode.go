package types

type RequestMode uint8

const (
	RequestModeUnknown RequestMode = iota
	RequestModeStream
	RequestModeNonStream
)

func RequestModeFromStream(stream bool) RequestMode {
	if stream {
		return RequestModeStream
	}
	return RequestModeNonStream
}

func (mode RequestMode) String() string {
	switch mode {
	case RequestModeStream:
		return "stream"
	case RequestModeNonStream:
		return "non-stream"
	default:
		return "unknown"
	}
}
