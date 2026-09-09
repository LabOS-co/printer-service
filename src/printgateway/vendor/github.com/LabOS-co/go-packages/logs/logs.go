package logs

import (
	"fmt"

	"github.com/sirupsen/logrus"
)

type LogPriority string

// Constant priority values
const (
	PRIORITY_DEBUG LogPriority = "DEBUG"
	PRIORITY_INFO  LogPriority = "INFO"
	PRIORITY_ERROR LogPriority = "ERROR"
)

type LogFormat string

const (
	FormatString LogFormat = "string"
	FormatJSON   LogFormat = "json"
)

type LogMode string

const (
	INFO  LogMode = "INFO"
	ERROR LogMode = "ERROR"
)

type Logger interface {
	LogDBQuery(query string, metadata *LogMetaData) error
	LogAPICompletion(metadata *LogMetaData) error
	LogInfo(message string, metadata *LogMetaData) error
	LogAPIError(message string, metadata *LogMetaData) error
	LogError(message string, metadata *LogMetaData) error
	LogDebug(message string, metadata *LogMetaData) error
	SetLogLevel(lvl string) error
	SetLogstashLogger(url string, port int) error
}

func GetLogger(settings Settings, loggerName string) (Logger, error) {
	log := createLogstashLogger(loggerName)
	log.environment = settings.GetEnvironment()
	logsSettings := &LogsSettings{}
	if err := settings.GetSettingsInPath("resource/log", logsSettings); err != nil {
		return nil, err
	}

	log.format = logsSettings.Format
	setLogstashLogger(log, logsSettings.Host, logsSettings.Port, loggerName)

	return log, nil
}

func GetLoggerWithSettings(logsSettings LogsSettings, loggerName string) (Logger, error) {
	log := createLogstashLogger(loggerName)
	log.format = logsSettings.Format
	setLogstashLogger(log, logsSettings.Host, logsSettings.Port, loggerName)

	return log, nil
}

func createLogstashLogger(loggerName string) *logstashLogger {
	log := &logstashLogger{
		logger:     logrus.New(),
		loggerName: loggerName,
	}

	// Set a different formatter (a simpler one) for the console output
	log.logger.SetFormatter(new(ConsoleFormatter))
	log.logger.SetLevel(logrus.InfoLevel)

	return log
}

func setLogstashLogger(log Logger, host string, port int, loggerName string) error {
	if host != "" {
		if port == 0 {
			port = defaultPort
		}

		if err := log.SetLogstashLogger(host, port); err != nil {
			return err
		}

		log.LogInfo(fmt.Sprintf("Logger initialized with url: %s and port: %d", host, port), &LogMetaData{Service: loggerName})
	}

	return nil
}

func GetConsoleLogger() Logger {
	return &consoleLogger{}
}
