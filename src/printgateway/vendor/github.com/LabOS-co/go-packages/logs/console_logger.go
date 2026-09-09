package logs

import (
	"fmt"
	"time"

	util "github.com/LabOS-co/go-packages/shared/utilities"
	"github.com/TwiN/go-color"
)

type consoleLogger struct{}

func (l *consoleLogger) LogDBQuery(query string, metadata *LogMetaData) error {
	return printMessageToConsole(PRIORITY_INFO, query)
}

func (l *consoleLogger) LogAPICompletion(metadata *LogMetaData) error {
	return printMessageToConsole(PRIORITY_INFO, metadata.SyslogMessage)
}

func (l *consoleLogger) LogInfo(message string, metadata *LogMetaData) error {
	return printMessageToConsole(PRIORITY_INFO, message)
}

func (l *consoleLogger) LogAPIError(message string, metadata *LogMetaData) error {
	return printMessageToConsole(PRIORITY_ERROR, message)
}

func (l *consoleLogger) LogError(message string, metadata *LogMetaData) error {
	return printMessageToConsole(PRIORITY_ERROR, message)
}

func (l *consoleLogger) LogDebug(message string, metadata *LogMetaData) error {
	return printMessageToConsole(PRIORITY_INFO, message)
}

func (l *consoleLogger) SetLogLevel(lvl string) error {
	return fmt.Errorf("log level is not supported in console logger")
}

func (l *consoleLogger) SetLogstashLogger(host string, port int) error {
	return fmt.Errorf("logstash logger is not supported in console logger")
}

func getColoredMessage(mode LogMode, message string) string {
	messageColor := color.White
	if mode == ERROR {
		messageColor = color.Bold + color.Red
	}
	return color.Colorize(messageColor, message)
}

func getConsoleFormattedMessage(priority LogPriority, message string) string {
	logMode := util.If(priority == PRIORITY_ERROR, ERROR, INFO)
	message = fmt.Sprintf("%s - %s\n", time.Now().Format("02/01/2006 15:04:05"), message)

	return getColoredMessage(logMode, message)
}

func printMessageToConsole(priority LogPriority, message string) error {
	fmt.Print(getConsoleFormattedMessage(priority, message))
	return nil
}
