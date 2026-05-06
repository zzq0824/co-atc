package transcription

// ProcessorInterface 定义了音频转写处理器的接口
type ProcessorInterface interface {
	Start() error
	Stop() error
}

// 确保处理器实现该接口
var _ ProcessorInterface = (*Processor)(nil)
