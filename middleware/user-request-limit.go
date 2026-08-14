package middleware

import (
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting"
	"github.com/gin-gonic/gin"
)

func UserRequestRateLimit() gin.HandlerFunc {
	return userRequestRateLimit(true)
}

func UserRequestConcurrencyLimit() gin.HandlerFunc {
	return userRequestRateLimit(false)
}

func userRequestRateLimit(checkTPM bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		concurrencyLimit := setting.GetUserConcurrentRequestLimit()
		var tokenLimit int64
		if checkTPM {
			tokenLimit = int64(setting.GetUserTokensPerMinuteLimit())
		}
		if concurrencyLimit <= 0 && tokenLimit <= 0 {
			c.Next()
			return
		}

		userID := c.GetInt("id")
		if userID <= 0 {
			abortWithOpenAiMessage(c, http.StatusUnauthorized, "user authentication is required")
			return
		}

		lease, limitKind, retryAfter, err := service.AcquireUserRequestLimitLease(
			c.Request.Context(),
			userID,
			concurrencyLimit,
			tokenLimit,
		)
		if err != nil {
			abortWithOpenAiMessage(c, http.StatusInternalServerError, "user request limit check failed")
			return
		}
		if limitKind != "" {
			retryAfterSeconds := int64((retryAfter + time.Second - 1) / time.Second)
			if retryAfterSeconds < 1 {
				retryAfterSeconds = 1
			}
			c.Header("Retry-After", strconv.FormatInt(retryAfterSeconds, 10))
			if limitKind == service.UserRequestLimitTPM {
				abortWithOpenAiMessage(
					c,
					http.StatusTooManyRequests,
					fmt.Sprintf("每用户 TPM 已达到上限（%d tokens/min），请在 %d 秒后重试", tokenLimit, retryAfterSeconds),
					types.ErrorCodeUserTPMLimit,
				)
				return
			}
			abortWithOpenAiMessage(
				c,
				http.StatusTooManyRequests,
				fmt.Sprintf("每用户并发请求数已达到上限（%d），请稍后重试", concurrencyLimit),
				types.ErrorCodeUserConcurrencyLimit,
			)
			return
		}

		if lease != nil {
			defer lease.Release()
		}
		c.Next()
	}
}
