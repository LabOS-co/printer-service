package logs

import (
	"encoding/json"
	"fmt"
	"net"
	"os"

	logrustash "github.com/bshuster-repo/logrus-logstash-hook"
	"github.com/sirupsen/logrus"
)

/*
The pattern of the syslog message is:
<{syslog_pri}>daemon::[{version}] {duration} {priority - TRACE|INFO|DEBUG} {logger}:{thread_id} {process_id} {session}|{session_code} {service} [{site}] {job_id} {environment} [{version}] {sequence_number} [{error_code}] no_error
*/
const basePattern = `<30>daemon::[3] %d %s %s:%d %d %d|%d %s [NO_VAL] %s %s [NO_VAL] %d [%d] %s`

/*  - [{db_name}] [{query_duration}] {message} */
const DBQueryPattern = /* 50004 */ ` - [%s] [%d] %s`

/* - API call completed: {service}. Status: {status}, Duration: {service_duration} ms. */
const APICompletedPattern = /* 50005 */ ` - API call completed: %s. Status: %s, Duration: %d ms`

const noVal = "NO_VAL"

// Error codes
const (
	ERROR_CODE_GENERAL        = 0
	ERROR_CODE_API_ERROR      = 1
	ERROR_CODE_DB_QUERY       = 50004
	ERROR_CODE_API_COMPLETION = 50005
)

// Constant error texts
const (
	API_EXCEPTION = "API call caught exception"
)

const defaultPort = 514

var seq = 0

type LogMetaData struct {
	SyslogMessage   string `json:"syslog_message"`
	ThreadId        int    `json:"thread_id"`
	Session         int    `json:"session"`
	SessionCode     int    `json:"session_code"`
	Service         string `json:"service"`
	ErrorText       string `json:"error_text"`
	JobId           string `json:"job_id"`
	Duration        int    `json:"duration"`
	DbName          string `json:"db_name"`
	QueryDuration   int    `json:"query_duration"`
	ServiceDuration int    `json:"service_duration"`
	Status          string `json:"status"`
}

type LogsSettings struct {
	Host   string    `json:"host"`
	Port   int       `json:"port,string"`
	Type   string    `json:"type"`
	Format LogFormat `json:"format"`
}

// We define this here to break circular dependency
// So we don't need to import settings package which uses logs package
type Settings interface {
	GetSettingsInPath(path string, settingsPtr any) error
	GetEnvironment() string
}

type logstashLogger struct {
	logger      *logrus.Logger
	loggerName  string
	environment string
	format      LogFormat
}

func (l *logstashLogger) SetLogLevel(level string) error {
	logLevel, err := logrus.ParseLevel(level)
	if err != nil {
		logLevel = logrus.InfoLevel
	}
	l.logger.SetLevel(logLevel)
	return err
}

func (l *logstashLogger) LogDBQuery(query string, metadata *LogMetaData) error {
	fields := l.getLogFields(query, PRIORITY_INFO, ERROR_CODE_DB_QUERY, metadata)

	ctx := l.logger.WithFields(fields)
	ctx.Info()

	return nil
}

func (l *logstashLogger) LogAPICompletion(metadata *LogMetaData) error {
	if l.logger == nil {
		return nil
	}

	fields := l.getLogFields("", PRIORITY_INFO, ERROR_CODE_API_COMPLETION, metadata)

	ctx := l.logger.WithFields(fields)
	ctx.Info()

	return nil
}

func (l *logstashLogger) LogDebug(message string, metadata *LogMetaData) error {
	fields := l.getLogFields(message, PRIORITY_DEBUG, ERROR_CODE_GENERAL, metadata)

	ctx := l.logger.WithFields(fields)
	ctx.Debug()

	return nil
}

func (l *logstashLogger) LogInfo(message string, metadata *LogMetaData) error {
	fields := l.getLogFields(message, PRIORITY_INFO, ERROR_CODE_GENERAL, metadata)

	ctx := l.logger.WithFields(fields)
	ctx.Info()

	return nil
}

func (l *logstashLogger) LogAPIError(message string, metadata *LogMetaData) error {
	metadata.ErrorText = API_EXCEPTION
	fields := l.getLogFields(message, PRIORITY_ERROR, ERROR_CODE_API_ERROR, metadata)

	ctx := l.logger.WithFields(fields)
	ctx.Error()

	return nil
}

func (l *logstashLogger) LogError(message string, metadata *LogMetaData) error {
	fields := l.getLogFields(message, PRIORITY_ERROR, ERROR_CODE_GENERAL, metadata)

	ctx := l.logger.WithFields(fields)
	ctx.Error()

	return nil
}

func (l *logstashLogger) SetLogstashLogger(url string, port int) error {
	conn, err := net.Dial("udp", fmt.Sprintf("%s:%d", url, port))
	if err != nil {
		return err
	}

	var formatter logrus.Formatter
	switch l.format {
	case FormatString:
		formatter = new(LabOSFormatter)
	default:
		formatter = new(JSONFormatter)
	}

	hook := logrustash.New(conn, formatter)
	l.logger.Hooks.Add(hook)

	return nil
}

func (l *logstashLogger) getLogFields(message string, priority LogPriority, errorCode int, metadata *LogMetaData) logrus.Fields {
	if metadata == nil {
		metadata = &LogMetaData{}
	}

	seq++
	pid := os.Getpid()
	host := os.Getenv("HOST_IP")
	hostname := os.Getenv("HOST_NAME")

	errorText := "no_error"
	if metadata.ErrorText != "" {
		errorText = metadata.ErrorText
	}

	return logrus.Fields{
		"logger":           l.loggerName,
		"thread_id":        metadata.ThreadId,
		"session":          metadata.Session,
		"session_code":     metadata.SessionCode,
		"service":          getStringOrNoVal(metadata.Service),
		"sequence_number":  seq,
		"process_id":       pid,
		"error_code":       errorCode,
		"error_text":       errorText,
		"priority":         priority,
		"job_id":           getStringOrNoVal(metadata.JobId),
		"duration":         metadata.Duration,
		"db_name":          getStringOrNoVal(metadata.DbName),
		"query_duration":   metadata.QueryDuration,
		"service_duration": metadata.ServiceDuration,
		"status":           getStringOrNoVal(metadata.Status),
		"syslog_message":   message,
		"environment":      l.environment,
		"host":             host,
		"host_name":        hostname,
	}
}

type ConsoleFormatter struct{}

func (f *ConsoleFormatter) Format(entry *logrus.Entry) ([]byte, error) {
	priority, ok := entry.Data["priority"].(LogPriority)
	if !ok {
		return nil, fmt.Errorf("priority is not a LogPriority")
	}

	message, ok := entry.Data["syslog_message"].(string)
	if !ok {
		return nil, fmt.Errorf("syslog_message is not a string")
	}

	return []byte(getConsoleFormattedMessage(LogPriority(priority), message)), nil
}

type LabOSFormatter struct{}

func (f *LabOSFormatter) Format(entry *logrus.Entry) ([]byte, error) {
	return []byte(getLogstashFormattedMessage(entry.Data)), nil
}

type JSONFormatter struct{}

func (f *JSONFormatter) Format(entry *logrus.Entry) ([]byte, error) {
	data := make(map[string]interface{}, len(entry.Data)+1)
	for k, v := range entry.Data {
		if strVal, ok := v.(string); ok && (strVal == "" || strVal == noVal) {
			continue
		}
		data[k] = v
	}
	if errorCode, ok := entry.Data["error_code"].(int); ok && errorCode == ERROR_CODE_API_COMPLETION {
		service, _ := entry.Data["service"].(string)
		status, _ := entry.Data["status"].(string)
		serviceDuration, _ := entry.Data["service_duration"].(int)
		data["syslog_message"] = fmt.Sprintf(APICompletedPattern[3:], service, status, serviceDuration)
	}

	data["ver"] = "5"

	payload, err := json.Marshal(data)
	if err != nil {
		return nil, err
	}

	line := fmt.Sprintf("<30>daemon:%s\n", payload)
	return []byte(line), nil
}

func getLogstashFormattedMessage(logMetaData logrus.Fields) string {
	pattern := fmt.Sprintf(
		basePattern,
		logMetaData["duration"],
		logMetaData["priority"],
		logMetaData["logger"],
		logMetaData["thread_id"],
		logMetaData["process_id"],
		logMetaData["session"],
		logMetaData["session_code"],
		logMetaData["service"],
		logMetaData["job_id"],
		logMetaData["environment"],
		logMetaData["sequence_number"],
		logMetaData["error_code"],
		logMetaData["error_text"],
	)

	errorCode := logMetaData["error_code"].(int)
	serviceDuration := logMetaData["service_duration"].(int)
	switch errorCode {
	case 50004:
		pattern += fmt.Sprintf(DBQueryPattern, logMetaData["db_name"], logMetaData["query_duration"], logMetaData["syslog_message"])
	case 50005:
		pattern += fmt.Sprintf(APICompletedPattern, logMetaData["service"], logMetaData["status"], serviceDuration)
	default: // Including 0
		pattern += fmt.Sprintf(" - %s", logMetaData["syslog_message"])
	}

	return pattern
}

func getStringOrNoVal(value string) string {
	if value == "" {
		return noVal
	}

	return value
}
