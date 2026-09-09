package error_handler

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/LabOS-co/go-packages/logs"
)

type APIError struct {
	StatusCode int
	Err        error
}

func (r APIError) Error() string {
	return r.Err.Error()
}

type ErrorDetails struct {
	Details   string `json:"details"`
	LogicCode int    `json:"logicCode,omitempty"`
	QuestionDetails
}

type QuestionDetails struct {
	AdditionalInfo   string `json:"additionalInfo,omitempty"`
	AnswerParameter  string `json:"answerParameter,omitempty"`
	DefaultAnswer    string `json:"defaultAnswer,omitempty"`
	ObjectIdentifier string `json:"objectIdentifier,omitempty"`
	QuestionCode     int    `json:"questionCode,omitempty"`
	QuestionText     string `json:"questionText,omitempty"`
}

type ErrorResponse struct {
	ErrorCode    string       `json:"errorCode,omitempty"`
	ErrorDetails ErrorDetails `json:"errorDetails"`
	ErrorMessage string       `json:"errorMessage"`
}

type ErrorHandler interface {
	SetMetaData(metaData *logs.LogMetaData)
	HandleError(err error, w http.ResponseWriter)
	HandleLogicError(err error, logicError int, w http.ResponseWriter)
	HandleQuestion(err error, questionDetails *QuestionDetails, w http.ResponseWriter)
	ThrowUnAuthorizedError(w http.ResponseWriter)
	ThrowBadParameterFormatError(w http.ResponseWriter, paramName string)
	ThrowMissingParameterError(w http.ResponseWriter, paramName string)
	ThrowMissingQueryParameterError(w http.ResponseWriter, paramName string)
	ThrowPermissionDeniedError(w http.ResponseWriter)
	ThrowInternalServerError(w http.ResponseWriter)
}

type errorHandler struct {
	Logger   logs.Logger
	MetaData *logs.LogMetaData
}

func NewErrorHandler(logger logs.Logger, metaData *logs.LogMetaData) ErrorHandler {
	return &errorHandler{
		Logger:   logger,
		MetaData: metaData,
	}
}

func (e *errorHandler) SetMetaData(metaData *logs.LogMetaData) {
	e.MetaData = metaData
}

func (e *errorHandler) HandleError(err error, w http.ResponseWriter) {
	e.logError(err)

	handleError(err, w)
}

func (e *errorHandler) HandleQuestion(err error, questionDetails *QuestionDetails, w http.ResponseWriter) {
	e.logError(err)

	handleQuestion(err, questionDetails, w)
}

func (e *errorHandler) HandleLogicError(err error, logicError int, w http.ResponseWriter) {
	e.logError(err)

	handleLogicError(err, logicError, w)
}

func (e *errorHandler) ThrowUnAuthorizedError(w http.ResponseWriter) {
	err := fmt.Errorf("authentication required")

	e.logError(err)

	handleError(APIError{
		StatusCode: http.StatusUnauthorized,
		Err:        err,
	}, w)
}

func (e *errorHandler) ThrowBadParameterFormatError(w http.ResponseWriter, paramName string) {
	err := fmt.Errorf("bad parameter format - %s", paramName)

	e.logError(err)

	handleError(APIError{
		StatusCode: http.StatusBadRequest,
		Err:        err,
	}, w)
}

func (e *errorHandler) ThrowMissingParameterError(w http.ResponseWriter, paramName string) {
	err := fmt.Errorf("missing url parameter: %s", paramName)

	e.logError(err)

	handleError(APIError{
		StatusCode: http.StatusBadRequest,
		Err:        err,
	}, w)
}

func (e *errorHandler) ThrowMissingQueryParameterError(w http.ResponseWriter, paramName string) {
	err := fmt.Errorf("missing query parameter: %s", paramName)

	e.logError(err)

	handleError(APIError{
		StatusCode: http.StatusBadRequest,
		Err:        err,
	}, w)
}

func (e *errorHandler) ThrowPermissionDeniedError(w http.ResponseWriter) {
	err := fmt.Errorf("permission denied")

	e.logError(err)

	handleError(APIError{
		StatusCode: http.StatusForbidden,
		Err:        err,
	}, w)
}

func (e *errorHandler) ThrowInternalServerError(w http.ResponseWriter) {
	err := fmt.Errorf("internal server error")

	e.logError(err)

	handleError(APIError{
		StatusCode: http.StatusInternalServerError,
		Err:        err,
	}, w)
}

func (e *errorHandler) logError(err error) {
	if e.Logger != nil {
		e.Logger.LogError(err.Error(), e.MetaData)
	}
}

const serverErrorMessage = "Internal server error"
const internalServerErrorCode = "21" // Needs to be a string to be consistent with C++ apis

func handleError(err error, w http.ResponseWriter) {
	if err == nil || w == nil {
		return
	}

	if reqErr, ok := err.(APIError); ok {
		handleAPIError(reqErr.Err, 0, nil, w, reqErr.StatusCode)
	} else {
		handleAPIError(err, 0, nil, w, 500)
	}
}

func handleQuestion(err error, questionDetails *QuestionDetails, w http.ResponseWriter) {
	if err == nil || questionDetails == nil || w == nil {
		return
	}

	if reqErr, ok := err.(APIError); ok {
		handleAPIError(reqErr.Err, 0, questionDetails, w, reqErr.StatusCode)
	} else {
		handleAPIError(err, 0, questionDetails, w, 500)
	}
}

func handleLogicError(err error, logicError int, w http.ResponseWriter) {
	if err == nil || w == nil {
		return
	}

	if reqErr, ok := err.(APIError); ok {
		handleAPIError(reqErr.Err, logicError, nil, w, reqErr.StatusCode)
	} else {
		handleAPIError(err, logicError, nil, w, 500)
	}
}

func handleAPIError(err error, logicError int, questionDetails *QuestionDetails, w http.ResponseWriter, statusCode int) {
	fmt.Printf("Error: %s\n", err.Error())

	w.Header().Set("Content-Type", "application/json")

	if statusCode != 0 {
		w.WriteHeader(statusCode)
	} else {
		w.WriteHeader(http.StatusInternalServerError)
	}

	errorDetails := ErrorDetails{
		Details:   err.Error(),
		LogicCode: logicError,
	}

	if questionDetails != nil {
		errorDetails.QuestionDetails = *questionDetails
	}

	errorResponse := ErrorResponse{
		ErrorCode:    internalServerErrorCode,
		ErrorMessage: serverErrorMessage,
		ErrorDetails: errorDetails,
	}

	json.NewEncoder(w).Encode(errorResponse)
}
