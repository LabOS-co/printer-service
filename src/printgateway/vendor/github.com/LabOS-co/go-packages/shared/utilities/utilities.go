package shared

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"

	"github.com/shirou/gopsutil/disk"
	"golang.org/x/text/encoding/unicode"
	"golang.org/x/text/transform"
)

const (
	B  = 1
	KB = 1024 * B
	MB = 1024 * KB
	GB = 1024 * MB
	TB = 1024 * GB
)

const (
	IPv4AddressRegexPattern = `^([0-9]|[1-9][0-9]|1[0-9]{2}|2[0-4][0-9]|25[0-5])\.([0-9]|[1-9][0-9]|1[0-9]{2}|2[0-4][0-9]|25[0-5])\.([0-9]|[1-9][0-9]|1[0-9]{2}|2[0-4][0-9]|25[0-5])\.([0-9]|[1-9][0-9]|1[0-9]{2}|2[0-4][0-9]|25[0-5])$`
	EthernetAdapterName     = "Ethernet"
)

type SortTypes string

const (
	SortDate SortTypes = "date"
	SortSize SortTypes = "size"
	SortName SortTypes = "name"
	SortExt  SortTypes = "ext"
)

type SortDirection int

const (
	Ascending SortDirection = iota
	Descending
)

type SortCondition struct {
	Condition SortTypes
	Direction SortDirection
}

func GetPointerToValue[T any](val T) *T {
	return &val
}

func GetPointerToStringValue(ptr *string) string {
	if ptr == nil {
		return ""
	}

	return *ptr
}

func GetPointerToBoolValue(ptr *bool) bool {
	if ptr == nil {
		return false
	}

	return *ptr
}

func GetStringQueryParam(key string, defaultValue string, w http.ResponseWriter, r *http.Request) string {
	if !r.URL.Query().Has(key) {
		return defaultValue
	}

	param := r.URL.Query().Get(key)
	return param
}

func GetBoolQueryParam(key string, defaultValue bool, w http.ResponseWriter, r *http.Request) (bool, error) {
	param := r.URL.Query().Get(key)
	if param == "" {
		return defaultValue, nil
	} else {
		val, err := strconv.ParseBool(param)
		if err != nil {
			return val, fmt.Errorf("parameter '%s' should contain a boolean value", key)
		}
		return val, nil
	}
}

func ExtractAuthenticationHeaders(r *http.Request) (string, string) {
	return r.Header.Get("X-Laas-Session-Token"), r.Header.Get("Authorization")
}

func GetExecutableFolderPath() (string, error) {
	exe, err := os.Executable()

	if err != nil {
		return "", err
	}

	return filepath.Dir(exe), nil
}

func BitsToMebibits(bits int) float64 {
	return float64(bits) / 1048576
}

func ReadFile(filePath string) ([]byte, error) {
	err := FileExist(filePath)
	if err != nil {
		return nil, err
	}

	// Open our file
	currFile, err := os.Open(filePath)
	if err != nil {
		return nil, err
	}

	// defer the closing of our jsonFile so that we can parse it later on
	defer currFile.Close()

	// read our opened file as a byte array.
	byteValue, err := io.ReadAll(currFile)
	if err != nil {
		return nil, nil
	}

	return byteValue, err
}

func ReadFileAsString(filePath string) (string, error) {
	byteValue, err := ReadFile(filePath)
	if err != nil {
		return "", err
	}

	return string(byteValue), nil
}

func SortFilesByCondition(filesInfo []os.FileInfo, condition *SortCondition) {
	slices.SortStableFunc(filesInfo, func(a, b os.FileInfo) int {
		switch condition.Condition {
		case SortDate:
			return compareFileByDate(a, b, condition.Direction)
		case SortSize:
			return compareFileBySize(a, b, condition.Direction)
		case SortName:
			return compareFileByName(a, b, condition.Direction)
		case SortExt:
			return compareFileByExtension(a, b, condition.Direction)
		default:
			return 0
		}
	})
}

func CopyFile(source, destination string) (int64, error) {
	srcFile, err := os.Open(source)
	if err != nil {
		return 0, err
	}
	defer srcFile.Close()

	dstFile, err := os.Create(destination)
	if err != nil {
		return 0, err
	}
	defer dstFile.Close()

	return io.Copy(dstFile, srcFile)
}

func CopyDir(source, destination string) error {
	return CopyDirWithProgress(source, destination, nil)
}

func CopyDirWithProgress(source, destination string, progressHandlerFunc func(uint64)) error {
	var err error
	var totalSize uint64
	if progressHandlerFunc != nil {
		totalSize, err = getFolderTotalSize(source) // Total size of all files to copy
		if err != nil {
			return fmt.Errorf("error getting total folder size: %s", err)
		}
	}

	var copiedSize int64 = 0
	var lastPercentage int = -1

	return filepath.Walk(source, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		relativePath, _ := filepath.Rel(source, path)
		destination := filepath.Join(destination, relativePath)

		if info.IsDir() {
			return os.MkdirAll(destination, info.Mode())
		}

		bytesCopied, err := CopyFile(path, destination)
		if err != nil {
			return err
		}

		if progressHandlerFunc != nil {
			copiedSize += bytesCopied
			currentPercentage := int(float64(copiedSize) / float64(totalSize) * 100)

			// Only call progressFn if the percentage has changed
			if currentPercentage != lastPercentage {
				progressHandlerFunc(uint64(currentPercentage))
				lastPercentage = currentPercentage
			}
		}

		return nil
	})
}

func MoveDirWithProgress(sourceFolderPath, destinationFolderPath string, progressHandlerFunc func(uint64)) error {
	if !FolderExist(sourceFolderPath) {
		return fmt.Errorf("source folder '%s' does not exist", sourceFolderPath)
	}

	if !FolderExist(destinationFolderPath) {
		err := os.Mkdir(destinationFolderPath, os.ModePerm)
		if err != nil {
			return fmt.Errorf("could not create destination folder in '%s': %s", destinationFolderPath, err)
		}
	}

	var err error
	var totalFilesAmount uint64
	if progressHandlerFunc != nil {
		totalFilesAmount, err = getTotalFileAmount(sourceFolderPath) // Total amount of all files to move
		if err != nil {
			return fmt.Errorf("error getting total file amount in path '%s': %s", sourceFolderPath, err)
		}
	}

	var movedFiles int64 = 0
	var lastPercentage int = -1
	return filepath.Walk(sourceFolderPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		relativePath, _ := filepath.Rel(sourceFolderPath, path)
		destination := filepath.Join(destinationFolderPath, relativePath)

		// No need to rename folders, just to create them for the next rename action
		if info.IsDir() {
			return os.MkdirAll(destination, info.Mode()) // We do not count folders
		} else {
			if err = os.Rename(path, destination); err != nil {
				return err
			}
		}

		if progressHandlerFunc != nil {
			movedFiles++
			currentPercentage := int(float64(movedFiles) / float64(totalFilesAmount) * 100)

			// Only call progressFn if the percentage has changed
			if currentPercentage != lastPercentage {
				progressHandlerFunc(uint64(currentPercentage))
				lastPercentage = currentPercentage
			}
		}

		return nil
	})
}

func MoveDir(sourceFolderPath, destinationFolderPath string) error {
	return MoveDirWithProgress(sourceFolderPath, destinationFolderPath, nil)
}

func RemoveFolderContents(folderPath string) error {
	if !FolderExist(folderPath) {
		return fmt.Errorf("folder '%s' does not exist", folderPath)
	}

	files, err := os.ReadDir(folderPath)
	if err != nil {
		return err
	}

	for _, file := range files {
		err = os.RemoveAll(filepath.Join(folderPath, file.Name()))
		if err != nil {
			return err
		}
	}

	return nil
}

func FolderExist(folderPath string) bool {
	if _, err := os.Stat(folderPath); os.IsNotExist(err) {
		return false
	}

	return true
}

func IsFolderEmpty(folderPath string) (bool, error) {
	folder, err := os.ReadDir(folderPath)
	if err != nil {
		return false, err
	}
	return len(folder) == 0, nil
}

func CreateDir(path string) error {
	if !FolderExist(path) {
		err := os.Mkdir(path, os.ModePerm)
		if err != nil {
			return err
		}
	}

	return nil
}

func FileExist(fileName string) error {
	_, err := os.Stat(fileName)
	return err
}

func DownloadFile(url, destination string) error {
	resp, err := http.Get(url)
	if err != nil {
		return (err)
	}

	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return fmt.Errorf("status %s", resp.Status)
	}

	// Create the file
	out, err := os.Create(destination)
	if err != nil {
		return fmt.Errorf("err: %s", err)
	}
	defer out.Close()

	// Write the body to file
	_, err = io.Copy(out, resp.Body)

	return err
}

// If Ternary operator
func If[T any](cond bool, vtrue, vfalse T) T {
	if cond {
		return vtrue
	}
	return vfalse
}

func Unzip(source, destination string) error {
	return UnzipWithProgress(source, destination, nil)
}

func UnzipWithProgress(zipFilename, destinationPath string, progressHandlerFunc func(uint64)) error {
	// Open the source filename for reading
	zipReader, err := zip.OpenReader(zipFilename)
	if err != nil {
		return err
	}
	defer zipReader.Close()

	var totalSize uint64 // Total size of all files to extract
	var collector uint64 // Collector of extracted files size
	var percent uint64   // Percentage of extracted files

	if progressHandlerFunc != nil {
		totalSize = getZipSize(zipReader.File)
	}

	// For each file in the archive
	for _, archiveReader := range zipReader.File {
		// Prepare to write the file
		finalPath := filepath.Join(destinationPath, archiveReader.Name)

		// Check if the file to extract is just a directory
		if isZippedDir(archiveReader.Name) {
			if err = os.MkdirAll(finalPath, 0755); err != nil {
				return err
			}

			// Continue to the next file in the archive
			continue
		}

		// Open the file in the archive
		archiveFile, err := archiveReader.Open()
		if err != nil {
			return err
		}
		defer archiveFile.Close()

		// Create all needed directories
		if os.MkdirAll(filepath.Dir(finalPath), 0755) != nil {
			return err
		}

		// Prepare to write the destination file
		destinationFile, err := os.OpenFile(finalPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, archiveReader.Mode())
		if err != nil {
			return err
		}
		defer destinationFile.Close()

		// Write the destination file
		if _, err = io.Copy(destinationFile, archiveFile); err != nil {
			return err
		} else if progressHandlerFunc != nil {
			collector += archiveReader.UncompressedSize64
			oldPercent := percent
			percent = (collector * 100) / totalSize
			if percent != oldPercent {
				progressHandlerFunc(percent)
			}
		}
	}

	return nil
}

// UnzipParallel extracts the files from a zip file to a destination folder in parallel
// It uses a progressHandlerFunc to report the progress of the extraction
// the maximum number of workers is set to half of the number of CPU cores
func UnzipParallel(zipFileName, destinationPath string, progressHandlerFunc func(uint64)) error {
	// Open the zip file
	reader, err := zip.OpenReader(zipFileName)
	if err != nil {
		return fmt.Errorf("cannot open zip file: %s", err)
	}
	defer reader.Close()

	// Calculate the total size of all files to extract
	totalSize := getZipSize(reader.File)

	var collector uint64 // Collector of extracted files size
	var percent uint64

	// Initialize a wait group for synchronizing goroutines
	var wg sync.WaitGroup

	// Create a buffered channel for controlling the number of concurrent workers
	// Maximum number of workers is set to half of the number of CPU cores
	// This is to prevent the program from running out of memory when extracting large files
	// And to prevent the program from using all the CPU cores and causing the system to slow down
	sem := make(chan struct{}, runtime.NumCPU()/2)

	// Variable to store any error that occurs during extraction
	errStr := ""

	// Loop through each file in the zip archive
	for _, zipFile := range reader.File {
		// Increment the wait group counter for each file
		wg.Add(1)

		// Acquire a semaphore slot, limiting the number of workers
		sem <- struct{}{}

		// Start a goroutine to extract the file concurrently
		go extractFile(zipFile, destinationPath, &wg, sem, &errStr)

		// Update progress and call the progressHandlerFunc
		collector += zipFile.UncompressedSize64
		oldPercent := percent
		percent = (collector * 100) / totalSize
		// Send the progress to the progressHandlerFunc only if the percentage has changed
		if percent != oldPercent {
			progressHandlerFunc(percent)
		}

		// Check for errors during extraction
		if errStr != "" {
			// If an error occurs, wait for all goroutines to finish, close the semaphore, and return the error
			// We need to wait for all goroutines to finish because if we return early, the program will gep panics
			// because the goroutines will try reading from the closed file
			wg.Wait()
			close(sem)
			return fmt.Errorf("error extracting file: %s", errStr)
		}
	}

	// Wait for all goroutines to finish
	wg.Wait()

	// Close the semaphore channel
	close(sem)

	return nil
}

func GetParsedIpAddress(remoteAddress string) (string, error) {
	ip, _, err := net.SplitHostPort(remoteAddress)
	if err != nil {
		return "", fmt.Errorf("could not parse ip address: %s", err)
	}

	ipAddress := net.ParseIP(ip)
	if ipAddress == nil {
		return "", fmt.Errorf("invalid ip address: %s", ip)
	}

	return ipAddress.String(), nil
}

func GetErrorExitStatus(err error) int {
	if err != nil {
		if exitError, ok := err.(*exec.ExitError); ok {
			if status, ok := exitError.Sys().(syscall.WaitStatus); ok {
				return status.ExitStatus()
			}
		} else {
			return 1
		}
	}
	return 0
}

func GetFileNameWithoutExtension(fileName string) string {
	extension := filepath.Ext(fileName)
	return strings.TrimSuffix(fileName, extension)
}

func CloseFile(file *os.File, isAlreadyClosed *bool) {
	if !*isAlreadyClosed {
		file.Close()
		*isAlreadyClosed = true
	}
}

func ConvertFileFromUTF16ToUTF8(fileName string) error {
	file, err := os.Open(fileName)
	if err != nil {
		return err
	}

	isFileClosed := false
	defer CloseFile(file, &isFileClosed)

	byteValue, err := io.ReadAll(file)
	if err != nil {
		return err
	}

	// Check if the file is UTF16
	if !IsUTF16(byteValue) {
		return nil
	}

	utf8Encoded, err := ConvertUTF16ToUTF8(byteValue)
	if err != nil {
		return err
	}

	CloseFile(file, &isFileClosed)
	return OverwriteFileSafely(fileName, utf8Encoded)
}

// ConvertUTF16ToUTF8 converts a byte array from UTF-16 to UTF-8
func ConvertUTF16ToUTF8(b []byte) ([]byte, error) {
	// To handle files with or without BOM, we use a more generic UTF-16 decoder which is BOM-aware.
	utf16bom := unicode.UTF16(unicode.BigEndian, unicode.UseBOM)

	// Create a transformer source with the input bytes.
	reader := bytes.NewReader(b)
	// Create a transformer that decodes UTF16-BOM to UTF8.
	unicodeReader := transform.NewReader(reader, utf16bom.NewDecoder())

	// Now, read the transformed bytes (which are now UTF-8) into a byte slice.
	decodedBytes, err := io.ReadAll(unicodeReader)
	if err != nil {
		return nil, err
	}

	return decodedBytes, nil
}

// Check only if is UTF16 with BOM
func IsUTF16(b []byte) bool {
	if len(b) < 2 {
		return false
	}
	return b[0] == 0xFF && b[1] == 0xFE || b[0] == 0xFE && b[1] == 0xFF
}

func ConvertFileFromUTF8ToUTF16(fileName string) error {
	file, err := os.Open(fileName)
	if err != nil {
		return err
	}

	isFileClosed := false
	defer CloseFile(file, &isFileClosed)

	byteValue, err := io.ReadAll(file)
	if err != nil {
		return err
	}

	// Check if the file is UTF16
	if IsUTF16(byteValue) {
		return nil
	}

	utf16Encoded, err := ConvertFromUTF8ToUTF16(byteValue)
	if err != nil {
		return err
	}

	CloseFile(file, &isFileClosed)
	return OverwriteFileSafely(fileName, utf16Encoded)
}

func ConvertFromUTF8ToUTF16(data []byte) ([]byte, error) {
	utf16Encoded, err := unicode.UTF16(unicode.LittleEndian, unicode.UseBOM).NewEncoder().Bytes(data)
	if err != nil {
		return nil, err
	}

	return utf16Encoded, nil
}

// Check only if is UTF16 with BOM
func IsFileUTF16(fileName string) (bool, error) {
	file, err := os.Open(fileName)
	if err != nil {
		return false, err
	}

	defer file.Close()

	byteValue, err := io.ReadAll(file)
	if err != nil {
		return false, err
	}

	if len(byteValue) < 2 {
		return false, nil
	}
	return byteValue[0] == 0xFF && byteValue[1] == 0xFE || byteValue[0] == 0xFE && byteValue[1] == 0xFF, nil
}

func OverwriteFileSafely(fileName string, data []byte) error {
	tempFileName := fileName + "Temp"
	tempFile, err := os.Create(tempFileName)
	if err != nil {
		return err
	}

	_, err = tempFile.Write(data)
	tempFile.Close()
	if err != nil {
		os.Remove(tempFileName)
		return err
	}

	// Delete the old file
	if err = os.Remove(fileName); err != nil {
		os.Remove(tempFileName)
		return err
	}

	// Rename the temp file to the original file
	return os.Rename(tempFileName, fileName)
}

// GetStringFieldValue gets the string value of a field in a struct
// If the given field name does not exist in the passed object, an error is returned
// If the given field name is not of type string, an error is returned
func GetStringFieldValue(obj any, fieldName string) (string, error) {
	// Get reflected value of the passed object
	v := reflect.ValueOf(obj)

	// If the value is a pointer, we need to get the value it points to
	if v.Kind() == reflect.Ptr {
		v = v.Elem()
	}

	// Check if the field exists in the passed object
	field := v.FieldByName(fieldName)
	if !field.IsValid() {
		return "", fmt.Errorf("field '%s' does not exist", fieldName)
	}

	// Check if the field is a string
	if field.Kind() != reflect.String {
		return "", fmt.Errorf("field '%s' is not a string", fieldName)
	}

	// Return the string value of the field
	return field.String(), nil
}

// SortSliceByField sorts a slice of structs by a field name (it mutates the passed slice)
// The field must be a string
// The direction can be either Ascending or Descending
// You can choose if you want to ignore case when sorting
//
// Example:
//
//	type Person struct {
//		Name string
//	}
//
//	persons := []Person{
//		{"Alice"},
//		{"Charlie"},
//		{"Bill"},
//		{"BOB"},
//	}
//
// SortSliceByField(persons, "Name", Ascending, true)
// fmt.Println("Sorted:", persons) \\ [{Alice} {"Bill"} {"BOB"} {Charlie}]
func SortSliceByField[T any](slice []T, fieldName string, direction SortDirection, ignoreCase bool) {
	sort.Slice(slice, func(i, j int) bool {
		iVal, err := GetStringFieldValue(slice[i], fieldName)
		if err != nil {
			return false
		}

		jVal, err := GetStringFieldValue(slice[j], fieldName)
		if err != nil {
			return false
		}

		if ignoreCase {
			iVal = strings.ToLower(iVal)
			jVal = strings.ToLower(jVal)
		}

		if direction == Ascending {
			return iVal < jVal
		} else {
			return iVal > jVal
		}
	})
}

type diskUsage struct {
	Total float64
	Used  float64
	Free  float64
}

func GetDiskUsage(path string) (*diskUsage, error) {
	info, err := disk.Usage(path)
	if err != nil {
		return nil, err
	}

	return &diskUsage{
		Total: float64(info.Total) / float64(GB),
		Used:  float64(info.Used) / float64(GB),
		Free:  float64(info.Free) / float64(GB),
	}, nil
}

// Run a command and capture the error message from the original command and return it as an error
func RunCommand(cmd *exec.Cmd) error {
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return fmt.Errorf("exit code %d: %s", exitErr.ProcessState.ExitCode(), stderr.String())
		}
		return fmt.Errorf(stderr.String())
	}
	return nil
}

// PRIVATE ///
func isZippedDir(name string) bool {
	return strings.HasSuffix(name, "/") || strings.HasSuffix(name, "\\")
}

func getZipSize(zipFiles []*zip.File) uint64 {
	var totalSize uint64
	for _, f := range zipFiles {
		totalSize += f.UncompressedSize64
	}

	return totalSize
}

func getFolderTotalSize(folderPath string) (uint64, error) {
	var totalSize uint64
	err := filepath.Walk(folderPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			totalSize += uint64(info.Size())
		}
		return nil
	})
	if err != nil {
		return 0, err
	}

	return totalSize, nil
}

func getTotalFileAmount(folderPath string) (uint64, error) {
	var totalFiles uint64
	if err := filepath.Walk(folderPath, func(path string, info os.FileInfo, err error) error {
		if !info.IsDir() {
			totalFiles++
		}
		return nil
	}); err != nil {
		return 0, err
	}

	return totalFiles, nil
}

// extractFile extracts a file from a zip archive to the specified destination.
func extractFile(zipFile *zip.File, dest string, wg *sync.WaitGroup, sem chan struct{}, errStr *string) {
	// Decrease the wait group counter when the function exits
	defer wg.Done()
	// Release a semaphore slot when the function exits
	// The semaphore holds a group of goroutines and when the group is full it is not possible to add another goroutine
	// to free up space, we take from the semaphore channel one of the messages,
	// thus freeing up space for the next goroutine
	defer func() { <-sem }()

	// Open the zip file
	rc, err := zipFile.Open()
	if err != nil {
		// If there is an error opening the zip file, set the error string and return
		*errStr = fmt.Sprintf("cannot open zip file %s: %s", zipFile.Name, err)
		return
	}
	defer rc.Close()

	// Create the destination path by joining the destination directory and the file name in the zip archive
	path := filepath.Join(dest, zipFile.Name)

	// Ensure the directory structure exists
	if err = os.MkdirAll(filepath.Dir(path), os.ModePerm); err != nil {
		*errStr = fmt.Sprintf("cannot create directory for file %s: %s", path, err)
		return
	}

	// If the file is a directory, create it and return
	if isZippedDir(zipFile.Name) {
		os.MkdirAll(path, zipFile.Mode())
		return
	}

	// Create the destination file
	outFile, err := os.Create(path)
	if err != nil {
		// If there is an error creating the file, set the error string and return
		*errStr = fmt.Sprintf("cannot create file %s: %s", path, err)
		return
	}
	defer outFile.Close()

	// Copy the content from the zip file to the destination file
	_, err = io.Copy(outFile, rc)
	if err != nil {
		// If there is an error copying the file content, set the error string and return
		*errStr = fmt.Sprintf("cannot copy file %s: %s", path, err)
	}
}

// CopyDirParallel copies files and directories from source to destination concurrently.
// It uses progressHandlerFunc to report the progress of the copying process.
func CopyDirParallel(src, dest string, progressHandlerFunc func(uint64)) error {
	// Variables for tracking progress
	var totalSize uint64
	var copiedSize int64 = 0
	var lastPercentage int = -1

	// Calculate total size of all files to copy if progress tracking is enabled
	var err error
	if progressHandlerFunc != nil {
		totalSize, err = getFolderTotalSize(src)
		if err != nil {
			return fmt.Errorf("error getting total folder size: %s", err)
		}
	}

	// Initialize wait group for synchronizing goroutines
	var wg sync.WaitGroup

	// Create a buffered channel for controlling the number of concurrent workers
	// Maximum number of workers is set to half of the number of CPU cores
	// This is to prevent the program from running out of memory when extracting large files
	// And to prevent the program from using all the CPU cores and causing the system to slow down
	sem := make(chan struct{}, runtime.NumCPU()/2)

	// Start copying the directory
	wg.Add(1)
	errStr := ""
	go copyDirParallel(src,
		dest,
		&wg,
		sem,
		progressHandlerFunc,
		&totalSize,
		&copiedSize,
		&lastPercentage,
		&errStr,
	)

	// Wait for all goroutines to finish
	wg.Wait()

	// Close the semaphore channel
	close(sem)

	// Check for errors during copying
	if errStr != "" {
		return fmt.Errorf("error copying directory: %s", errStr)
	}

	// Calculate progress one last time before completion
	calculateProgress(progressHandlerFunc, totalSize, copiedSize, lastPercentage)
	progressHandlerFunc(100)
	return nil
}

// calculateProgress calculates and reports the progress of the copying process.
func calculateProgress(progressHandlerFunc func(uint64), totalSize uint64, copiedSize int64, lastPercentage int) {
	if progressHandlerFunc != nil {
		currentPercentage := int(float64(copiedSize) / float64(totalSize) * 100)

		// Only call progressHandlerFunc if the percentage has changed
		if currentPercentage != lastPercentage {
			progressHandlerFunc(uint64(currentPercentage))
			lastPercentage = currentPercentage
		}
	}
}

// copyDirParallel copies files and directories from source to destination concurrently.
func copyDirParallel(src, dest string,
	wg *sync.WaitGroup,
	sem chan struct{},
	progressHandlerFunc func(uint64),
	totalSize *uint64,
	copiedSize *int64,
	lastPercentage *int,
	errStr *string,
) {
	// Decrease the wait group counter when the function exits
	defer wg.Done()

	// Walk through the source directory
	filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			*errStr = fmt.Sprintf("error walking through the source directory: %s", err)
			return nil
		}

		// Calculate the relative path and destination path
		relativePath, _ := filepath.Rel(src, path)
		destPath := filepath.Join(dest, relativePath)

		// If it's a directory, create it in the destination
		if info.IsDir() {
			os.MkdirAll(destPath, info.Mode())
		} else {
			// For files, start a goroutine to copy the file concurrently
			wg.Add(1)
			sem <- struct{}{} // Acquire a semaphore slot, limiting the number of workers
			go copyFileParallel(path, destPath, wg, sem, copiedSize, errStr)
			if *errStr != "" {
				return nil
			}
			calculateProgress(progressHandlerFunc, *totalSize, *copiedSize, *lastPercentage)
		}
		return nil
	})
}

// copyFileParallel copies a file from source to destination concurrently.
func copyFileParallel(src, dest string, wg *sync.WaitGroup, sem chan struct{}, bytesCopied *int64, errStr *string) {
	// Decrease the wait group counter when the function exits
	defer wg.Done()
	// Release a semaphore slot when the function exits
	defer func() { <-sem }()

	// Open the source file
	sourceFile, err := os.Open(src)
	if err != nil {
		*errStr = fmt.Sprintf("cannot open source file %s: %s", src, err)
		return
	}
	defer sourceFile.Close()

	// Create the destination file
	destFile, err := os.Create(dest)
	if err != nil {
		*errStr = fmt.Sprintf("cannot create destination file %s: %s", dest, err)
		return
	}
	defer destFile.Close()

	// Copy the content from the source file to the destination file
	written, err := io.Copy(destFile, sourceFile)
	*bytesCopied += written
	if err != nil {
		*errStr = fmt.Sprintf("cannot copy file %s: %s", src, err)
		return
	}
}

func GetCurrentIpv4Address() (string, error) {
	// Get all network interfaces on the machine
	interfaces, err := net.Interfaces()
	if err != nil {
		return "", fmt.Errorf("could not get network interfaces: %s", err)
	}

	for _, iface := range interfaces {
		// Skip non-ethernet interfaces
		if iface.Name != EthernetAdapterName {
			continue
		}

		// Get the addresses for the interface
		addrs, err := iface.Addrs()
		if err != nil {
			return "", fmt.Errorf("could not get addresses for interface '%s': %s", iface.Name, err)
		}

		for _, addr := range addrs {
			// Remove the subnet mask from the address
			parts := strings.Split(addr.String(), "/")
			if len(parts) != 2 {
				continue
			}

			// Check if the address is an IPv4 address, if not, skip it
			ipv4Regex := regexp.MustCompile(IPv4AddressRegexPattern)
			if !ipv4Regex.MatchString(parts[0]) {
				continue
			}

			return parts[0], nil
		}
	}

	return "", fmt.Errorf("could not find an IPv4 address")
}

func compareFileByDate(a, b os.FileInfo, direction SortDirection) int {
	retVal := 0
	if a.ModTime().Before(b.ModTime()) {
		retVal = -1
	}

	if a.ModTime().After(b.ModTime()) {
		retVal = 1
	}

	if direction == Descending {
		return retVal * -1
	}

	return retVal
}

func compareFileByName(a, b os.FileInfo, direction SortDirection) int {
	retVal := 0

	aName := strings.ToLower(a.Name())
	bName := strings.ToLower(b.Name())
	if aName < bName {
		retVal = -1
	}
	if aName > bName {
		retVal = 1
	}

	if direction == Descending {
		return retVal * -1
	}

	return retVal
}

func compareFileBySize(a, b os.FileInfo, direction SortDirection) int {
	retVal := 0

	if a.Size() < b.Size() {
		retVal = -1
	}
	if a.Size() > b.Size() {
		retVal = 1
	}

	if direction == Descending {
		return retVal * -1
	}

	return retVal
}

func compareFileByExtension(a, b os.FileInfo, direction SortDirection) int {
	retVal := 0

	if filepath.Ext(a.Name()) < filepath.Ext(b.Name()) {
		retVal = -1
	}
	if filepath.Ext(a.Name()) < filepath.Ext(b.Name()) {
		retVal = 1
	}

	if direction == Descending {
		return retVal * -1
	}

	return retVal
}
