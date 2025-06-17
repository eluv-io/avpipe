package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/eluv-io/avpipe/pkg/validate"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Printf("Usage: %s <mp4_file_or_directory>\n", os.Args[0])
		fmt.Println("Validates fMP4 segment files for:")
		fmt.Println("  - Uniform sample durations")
		fmt.Println("  - Sequential MFHD sequence numbers (starting from 1)")
		fmt.Println("  - Consistent TFDT base media decode times")
		os.Exit(1)
	}

	target := os.Args[1]
	
	// Check if target is a file or directory
	info, err := os.Stat(target)
	if err != nil {
		log.Fatalf("Error accessing %s: %v", target, err)
	}

	if info.IsDir() {
		// Validate all MP4 files in directory
		err = validateDirectory(target)
	} else {
		// Validate single file
		err = validate.ValidateMezFile(target)
	}

	if err != nil {
		log.Fatalf("Validation failed: %v", err)
	}
}

// validateDirectory validates all MP4 files in a directory
func validateDirectory(dirPath string) error {
	files, err := os.ReadDir(dirPath)
	if err != nil {
		return fmt.Errorf("failed to read directory: %v", err)
	}

	mp4Files := []string{}
	for _, file := range files {
		if !file.IsDir() && strings.HasSuffix(strings.ToLower(file.Name()), ".mp4") {
			mp4Files = append(mp4Files, filepath.Join(dirPath, file.Name()))
		}
	}

	if len(mp4Files) == 0 {
		return nil
	}

	for _, file := range mp4Files {
		result, err := validate.ValidateMez(file)
		if err != nil {
			continue
		}

		validate.PrintMezValidationResult(os.Stdout, file, result)
	}

	return nil
}