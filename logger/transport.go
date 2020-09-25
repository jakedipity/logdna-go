package logger

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sync"
	"time"
)

type transport struct {
	key     string
	buffer  []Message
	options Options
	done    chan struct{}

	mu sync.Mutex
	wg sync.WaitGroup
}

func newTransport(options Options, key string) *transport {
	t := transport{
		key:     key,
		options: options,
		done:    make(chan struct{}),
	}

	go t.flushInterval()

	return &t
}

func (t *transport) close() {
	t.flush()

	close(t.done)
	t.wg.Wait()
}

func (t *transport) add(msg Message) {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.buffer = append(t.buffer, msg)

	if len(t.buffer) >= t.options.MaxBufferLen {
		t.flushSend()
	}
}

func (t *transport) flush() {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.flushSend()
}

func (t *transport) flushInterval() {
	ticker := time.NewTicker(t.options.FlushInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			t.flush()
		case <-t.done:
			return
		}
	}
}

func (t *transport) flushSend() {
	msgs := t.buffer
	t.buffer = t.buffer[:0]

	if len(msgs) == 0 {
		return
	}

	t.wg.Add(1)
	go func() {
		t.send(msgs)
		t.wg.Done()
	}()
}

func (t *transport) send(msgs []Message) error {
	var lines []Line
	for _, msg := range msgs {
		line := Line{
			Body:  msg.Body,
			App:   msg.MessageOptions.Options.App,
			Env:   msg.MessageOptions.Options.Env,
			Level: msg.MessageOptions.Options.Level,
		}

		timestamp := msg.MessageOptions.Timestamp
		line.Timestamp = timestamp.UnixNano() / int64(time.Millisecond)

		if msg.MessageOptions.Options.Meta != "" {
			line.Meta = metaEnvelope{
				indexed: msg.MessageOptions.Options.IndexMeta,
				meta:    msg.MessageOptions.Options.Meta,
			}
		}

		lines = append(lines, line)
	}

	payload := Payload{
		APIKey:     t.key,
		Hostname:   t.options.Hostname,
		IPAddress:  t.options.IPAddress,
		MacAddress: t.options.MacAddress,
		Tags:       t.options.Tags,
		Lines:      lines,
	}

	pbytes, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	req, err := http.NewRequest("POST", t.options.IngestURL, bytes.NewBuffer(pbytes))
	req.Header.Set("user-agent", os.Getenv("USERAGENT"))
	req.Header.Set("apikey", t.key)
	req.Header.Set("Content-type", "application/json")

	client := &http.Client{Timeout: t.options.SendTimeout}
	resp, err := client.Do(req)

	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 500 {
		return fmt.Errorf("Server error: %d", resp.StatusCode)
	}

	return nil
}
