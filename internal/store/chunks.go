package store

import "time"

// MemoryChunk is the embedding of a stretch of a conversation, the messages
// with Seq from StartSeq to EndSeq (inclusive) that occurred from StartAt to
// EndAt, made by Model. TextHash identifies the text that was embedded; the
// text itself is not stored.
type MemoryChunk struct {
	ID              int64
	ConversationKey string
	StartSeq        int64
	EndSeq          int64
	StartAt         time.Time
	EndAt           time.Time
	TextHash        string
	Model           string
	Vector          []float32
}

// ChunkState is how far a conversation's messages have been cut into chunks:
// every message up to CursorSeq has been, with Model the embedding model of
// the conversation's current chunks.
type ChunkState struct {
	CursorSeq int64
	Model     string
}

// ChunkRebuild asks for the chunks of a conversation between StartSeq and
// EndSeq (inclusive) to be cut and embedded again, after forgetting or a
// change of model.
type ChunkRebuild struct {
	ID       int64
	StartSeq int64
	EndSeq   int64
}

// ChunkWrite stores chunks of one conversation, all made by Model. It applies
// only while the conversation's forget stamp is still ForgetStamp, so that
// chunks cut before a member was forgotten are never stored. Each of Rebuilt
// first has the Model chunks within its range deleted and is then removed
// from the queue. A CursorSeq > 0 moves the conversation's cursor forward to
// it.
type ChunkWrite struct {
	ConversationKey string
	Model           string
	ForgetStamp     string
	Chunks          []MemoryChunk
	Rebuilt         []ChunkRebuild
	CursorSeq       int64
}

// ChunkStamp summarises the chunks of one conversation and model: Count of
// them, the highest ID MaxID, and UpTo, how many have an ID up to the one
// asked about. Chunk IDs are never reused, so a cache holding the chunks up
// to an ID is current when UpTo still equals its size.
type ChunkStamp struct {
	Count int
	MaxID int64
	UpTo  int
}
