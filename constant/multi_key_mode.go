package constant

type MultiKeyMode string

const (
	MultiKeyModeRandom        MultiKeyMode = "random"         // 随机
	MultiKeyModePolling       MultiKeyMode = "polling"        // 轮询
	MultiKeyModeAffinity      MultiKeyMode = "affinity"       // 缓存亲和
	MultiKeyModeLeastRequests MultiKeyMode = "least_requests" // 近期最少请求
)
