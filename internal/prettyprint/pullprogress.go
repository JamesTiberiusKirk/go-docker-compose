package prettyprint

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
)

type progressDetail struct {
	Current int64 `json:"current"`
	Total   int64 `json:"total"`
}

type pullEvent struct {
	ID             string         `json:"id"`
	Status         string         `json:"status"`
	Progress       string         `json:"progress"`
	ProgressDetail progressDetail `json:"progressDetail"`
}

// PrintPullProgress reads from the Docker image pull response and displays it cleanly.
func PrintPullProgress(r io.Reader) error {
	scanner := bufio.NewScanner(r)
	statusMap := make(map[string]string)

	for scanner.Scan() {
		var evt pullEvent
		if err := json.Unmarshal(scanner.Bytes(), &evt); err != nil {
			// If the line is not JSON, skip it.
			continue
		}

		// Skip duplicate or empty
		if evt.ID == "" && evt.Status != "" {
			fmt.Println(evt.Status)
			continue
		}
		if evt.ID == "" {
			continue
		}

		// Compose display string
		line := fmt.Sprintf("[%s] %s", evt.ID, evt.Status)
		if evt.Progress != "" {
			line += " " + evt.Progress
		}

		// Only update if status actually changed
		prev := statusMap[evt.ID]
		if prev != line {
			statusMap[evt.ID] = line
			fmt.Println(line)
		}
	}

	return scanner.Err()
}
