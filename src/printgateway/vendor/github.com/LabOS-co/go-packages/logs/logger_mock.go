package logs

type LoggerMock struct{}

func (LoggerMock) Init(url string, port int, loggerName string) {}

func (LoggerMock) LogDBQuery(query string, metadata *LogMetaData) error {
	return nil
}

func (LoggerMock) LogAPICompletion(metadata *LogMetaData) error {
	return nil
}

func (LoggerMock) LogInfo(message string, metadata *LogMetaData) error {
	return nil
}

func (LoggerMock) LogAPIError(message string, metadata *LogMetaData) error {
	return nil
}

func (LoggerMock) LogError(message string, metadata *LogMetaData) error {
	return nil
}

func (LoggerMock) LogDebug(message string, metadata *LogMetaData) error {
	return nil
}

func (LoggerMock) SetLogLevel(lvl string) error {
	return nil
}

func (LoggerMock) SetLogstashLogger(url string, port int) error {
	return nil
}
