package conloomapi

const (
	CodeUnauthorized          = "UNAUTHORIZED"
	CodeForbidden             = "FORBIDDEN"
	CodeInvalidEvent          = "INVALID_EVENT"
	CodeDuplicateEvent        = "DUPLICATE_EVENT"
	CodeRateLimited           = "RATE_LIMITED"
	CodeQueueFull             = "QUEUE_FULL"
	CodeContextTooLarge       = "CONTEXT_TOO_LARGE"
	CodeContentBlocked        = "CONTENT_BLOCKED"
	CodeAttachmentUnsupported = "ATTACHMENT_UNSUPPORTED"
	CodeModelUnavailable      = "MODEL_UNAVAILABLE"
	CodeTaskCancelled         = "TASK_CANCELLED"
	CodeInternal              = "INTERNAL_ERROR"
	CodeNotFound              = "NOT_FOUND"
	CodeMemoryDisabled        = "MEMORY_DISABLED"
)

type ErrorBody struct {
	Code         string `json:"code"`
	Message      string `json:"message"`
	Retryable    bool   `json:"retryable"`
	RetryAfterMS int64  `json:"retry_after_ms"`
	TraceID      string `json:"trace_id"`
}

type ErrorResponse struct {
	Error ErrorBody `json:"error"`
}
