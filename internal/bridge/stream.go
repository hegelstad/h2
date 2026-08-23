package bridge

import "context"

// Streamer is the capability for bridges that can stream a message as
// incremental writes and then persist once on Close. Telegram
// implements this as an in-memory buffer plus one sendMessage.
type Streamer interface {
	OpenStream(ctx context.Context) (MessageStream, error)
}

// MessageStream is one in-flight outbound stream.
type MessageStream interface {
	Write(p []byte) (int, error)
	Close() error
	// Done is closed when the stream ends, including self-close on abandon.
	Done() <-chan struct{}
}
