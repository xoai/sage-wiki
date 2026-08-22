package extract

import (
	"fmt"
	"io"
	"net/mail"
	"os"
	"strings"
)

// extractEmail extracts text from an .eml email file.
func extractEmail(path, sourceType string) (*SourceContent, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("extract email: %w", err)
	}
	defer f.Close()

	msg, err := mail.ReadMessage(f)
	if err != nil {
		// Not a valid .eml — fall back to plain text
		f.Seek(0, 0)
		data, _ := io.ReadAll(f)
		return &SourceContent{
			Path: path,
			Type: orDefaultType(sourceType, "article"),
			Text: string(data),
		}, nil
	}

	var text strings.Builder

	// Headers
	from := msg.Header.Get("From")
	to := msg.Header.Get("To")
	subject := msg.Header.Get("Subject")
	date := msg.Header.Get("Date")

	text.WriteString(fmt.Sprintf("From: %s\n", from))
	text.WriteString(fmt.Sprintf("To: %s\n", to))
	text.WriteString(fmt.Sprintf("Date: %s\n", date))
	text.WriteString(fmt.Sprintf("Subject: %s\n\n", subject))

	// Body
	body, err := io.ReadAll(msg.Body)
	if err != nil {
		return nil, fmt.Errorf("extract email: read body: %w", err)
	}

	text.Write(body)

	return &SourceContent{
		Path: path,
		Type: orDefaultType(sourceType, "article"),
		Text: strings.TrimSpace(text.String()),
	}, nil
}
