package constant

type MultiKeyMode string

const (
	MultiKeyModeRandom                     MultiKeyMode = "random"                        // 随机
	MultiKeyModePolling                    MultiKeyMode = "polling"                       // 轮询
	MultiKeyModeAffinity                   MultiKeyMode = "affinity"                      // 缓存亲和
	MultiKeyModeLeastRequests              MultiKeyMode = "least_requests"                // 近期最少请求
	MultiKeyModeCacheAffinityLeastRequests MultiKeyMode = "cache_affinity_least_requests" // 模拟缓存亲和 + 近期最少请求
)
