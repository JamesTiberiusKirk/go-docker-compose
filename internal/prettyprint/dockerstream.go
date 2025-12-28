package prettyprint

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
)

// dockerStreamEvent represents a generic event from a Docker API stream (pull, build, etc.).
// It typically contains 'stream' for log output, 'status' for progress, and 'error' for build/pull failures.
type dockerStreamEvent struct {
	Stream         string `json:"stream"`
	Status         string `json:"status"`
	ID             string `json:"id"`
	Progress       string `json:"progress"`
	ProgressDetail struct {
		Current int64 `json:"current"`
		Total   int64 `json:"total"`
	} `json:"progressDetail"`
	Error string `json:"error"` // This field will contain build/pull errors
}

// PrintDockerStreamProgress reads from a Docker API response stream (like ImageBuild or ImagePull)
// and prints the progress or logs to stdout. It also checks for and returns any errors reported
// in the stream.
func PrintDockerStreamProgress(r io.Reader) error {
	scanner := bufio.NewScanner(r)
	statusMap := make(map[string]string) // To track and update multi-line progress efficiently

	for scanner.Scan() {
		line := scanner.Bytes()
		var evt dockerStreamEvent
		if err := json.Unmarshal(line, &evt); err != nil {
			// If it's not a JSON line, assume it's raw stream output and print it directly.
			// This often happens at the very end or for initial status messages.
			fmt.Println(string(line))
			continue
		}

		// Check for an error message in the stream
		if evt.Error != "" {
			return fmt.Errorf("Docker stream error: %s", evt.Error)
		}

		// Handle build stream output (often just 'stream' or 'status' messages)
		if evt.Stream != "" {
			fmt.Print(evt.Stream) // Print as is, Docker often adds newlines
			continue
		}

		// Handle progress-like updates (common in image pulls, sometimes builds)
		if evt.ID != "" || evt.Status != "" {
			displayLine := ""
			if evt.ID != "" {
				displayLine = fmt.Sprintf("[%s] %s", evt.ID, evt.Status)
			} else {
				displayLine = evt.Status
			}

			if evt.Progress != "" {
				displayLine += " " + evt.Progress
			}

			// Update status only if it's different from the previous one for that ID
			prev := statusMap[evt.ID]
			if prev != displayLine {
				statusMap[evt.ID] = displayLine
				fmt.Println(displayLine)
			}
		}
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("error reading Docker stream: %w", err)
	}

	return nil
}
