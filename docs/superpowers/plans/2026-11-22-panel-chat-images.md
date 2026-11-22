# Panel Chat Images Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** In the panel chat (`#/im`, also the classic chat page), the admin can attach 1–4 PNG/JPEG images to a message (attach button, paste, drag-and-drop, removable previews; image-only messages allowed). The bot sees the images of the current turn when its model takes images. When it is routed to a text-only model, a vision description stands in for each image, or an explicit "can't see" placeholder. Older messages stay text placeholders and are never sent again. Withdraw and deletion remove the images, and uploads that were never sent are purged after 24 h. Text-only requests stay byte-identical.

**Architecture:** `imaging.ProcessChat` makes two JPEG copies of an upload and keeps no original: a view copy (≤1536) and a reply copy (≤768). The panel stores them in the new `chat_images` table (migration 0023), owned by a conversation, through `POST conversations/{key}/images` and serves them through `GET conversations/{key}/images/{id}`. `SubmitPanel` takes image IDs, checks them under the chat's panel lock and stores them as `Attachment{Type:"image", ID}`. When SQLite inserts a panel message, it claims those rows (sets `message_id`) in the same transaction. `deleteMessagesFrom` (withdraw) and `DeleteConversation` (chat and bot deletion) delete the claimed rows, and the cleanup loop purges unclaimed rows after 24 h. The runner loads the reply copies of the current trigger messages (newest 4) into `contextbuilder.Input.CurrentImages`, plus cached descriptions for the other image attachments (`ImageDescriptions`). `Build` then puts the final user message into ordered `openai.ChatPart`s: the appearance label and image if they share the message, then each chat-image label and image, then the text. The text renders the current images as `[图片N]`. Before `Build`, `call` checks whether the profile lacks the `image` modality. If it does, `blindImages` swaps the seen images for cached or freshly described text (vision profile, which must also allow sensitive content) or for `[图片（看不到内容）]` plus a note. `WithoutImage` now strips every image from every message. The state step, suggestions, memory and search stay text-only.

**Tech Stack:** Go (module `github.com/kinako404/conloom`), SQLite via `internal/store/sqlite` (migration `0023_chat_images.sql`), stdlib `image/*` in `internal/imaging`, vanilla JS in `internal/panel/web`. No new dependencies.

**Spec:** `docs/superpowers/specs/2026-11-22-panel-chat-images-design.md`. Read it before your task, including "Builder-check decisions", which are binding.

## Global Constraints

- Public repo, fake data only: bots `bot_test`/`qq-main`, users `20001`/`29999`, panel user `admin`, names 小明/小红, `example.invalid`. Tests generate images in code (`image.NewRGBA` + `png.Encode`/`jpeg.Encode`). Never commit an image file.
- Commit messages: English, conventional style, exactly as given per task.
- Before every commit, `gofmt -l .` prints nothing and `go vet ./...` passes. Run tests with `CGO_ENABLED=1 GOFLAGS=-mod=readonly go test -race -count=1 <pkgs>`. Every changed JS file must pass `/usr/local/node/bin/node --check <file>`. `./internal/core/` takes about 6–7 minutes, so iterate with `-run` and run the whole package once before committing. The last task runs the full `./...`.
- Text-only requests stay byte-identical: no `Parts` anywhere, the same `Content`/`Images` as today, the same system prompt (`internal/persona/core.md` is **not** changed), and the same estimates. The same holds for the persona appearance image alone. A text-only `POST messages` stores `Attachments: []` as today. Every existing test passes unmodified, with two exceptions: `keyedTables` in `internal/store/sqlite/delete_test.go` (T1) and the `fakeChat.SubmitPanel` signature in `internal/panel/chat_test.go` (T8).
- Limits:
  - Formats and size: PNG/JPEG only, sniffed. At most `imaging.MaxBytes` (8 MiB), `MaxEdge` 8000 px and `MaxPixels` 25 Mpx.
  - Count: 1–4 images per message (`conloomapi.MaxPanelMessageImages = 4`). At most 4 per turn across merged messages (`maxTurnImages = 4`, the newest kept).
  - Copies: reply copy long edge `imaging.ReplyEdge` (768); view copy `imaging.ViewEdge` (1536). Both are JPEG quality 85.
  - Retention: unsent uploads are purged after `chatImageGrace = 24 * time.Hour`.
  - Describing: `chatDescribeTimeout = 30 * time.Second` for all descriptions of one request; descriptions are cut to `chatDescriptionMaxRunes = 200`.
- Fixed copy (used verbatim):
  - `contextbuilder.ChatImageLabel(n)` = `fmt.Sprintf("【对方发来的第 %d 张图片，即消息里的[图片%d]】", n, n)`.
  - Placeholders:
    - A current image the model sees: `[图片N]`, where N is its 1-based position among the seen images.
    - A described image: `[图片：<描述>]`, with the description on one line.
    - A current image that is neither seen nor described: `[图片（看不到内容）]`.
    - Any other image attachment: `[图片]` (as today).
  - `imagesUnseenNote` = `（标着“看不到内容”的图片你没有收到，不知道里面是什么，不要假装看到了。）`. It is added once, on its own line after the current messages and before the directive, whenever any current image renders as unseen.
  - `chatDescribePrompt` = `这是聊天里对方发来的一张图片。请用一到两句话客观地描述图里有什么：主要的人或物、场景，以及能看清的文字要点。不猜测、不评价，看不清的不写。只输出描述本身，不要开场白。`
  - Errors and audit:
    - `core.ErrBadImages` = `errors.New("core: a message carries at most 4 images, each uploaded to this chat and not sent yet")`.
    - Panel 400 for an upload to a conversation that is not a private panel chat: `只能在面板私聊里发图片`.
    - Audit action `panel.chat.image.upload`.
    - Log messages `chat image described` and `chat image not described`, with `image`, `role`, `latency_ms` and `reason`. Never log image bytes or data URLs.
  - UI: `发送图片`, `移除图片`, `上传中…`, `查看图片`, `一条消息最多 4 张图片`, `只支持 PNG 和 JPEG 图片`, `` `图片不能超过 ${MAX_IMAGE_MIB} MiB` ``. Chat list preview: `[图片]`, plus a space and the text when the message has text.
- Prefer new test files. Helpers added by T6 (`putChatImage`, `chatImageParts`) are reused by T9.
- Update every comment or doc that describes changed behaviour, including:
  - `imaging` package doc, `ChatMessage`, `Input`, `Build`, `BaseTokens`, `WithoutImage`, `renderPlaceholders`;
  - `SubmitPanel`, `WithdrawPanel`, `DeletePanelChat`, `call`, `stateInput`, `suggestTranscript`, `DescribeAppearance`;
  - the store docs of `DeleteConversation` and `DeleteMessagesFrom`, `uploadImage`, `postChatMessage`, `messageView`, `chatLast`.

## Review Focus

1. **Appearance image and user images in one message**: with no history, the persona's appearance image and the user's photos land on the same (first and current) user message. Parts are ordered as `AppearanceLabel`, appearance image, `ChatImageLabel(1)`, photo 1, …, then the text. Every image has its own label, so the model cannot take the photo for its own looks. When history exists, the appearance stays on the first message in today's form. Owners: T4 `TestBuild_ChatImagesWithAppearanceSameMessage` and `TestBuild_ChatImagesWithAppearanceEarlierMessage`; T6 `TestChatImages_WithAppearanceInFirstMessage`.
2. **The model can't see the images**: this covers a re-route to text-only `long_context` after the image model fails, and a turn too large for the image profile. Each image is described once by `vision` (view copy) and cached; the text model gets `[图片：…]` and no `image_url`. With no vision profile, a vision profile without `allow_sensitive_content`, or a failed or late description, the text model gets `[图片（看不到内容）]` plus `imagesUnseenNote`, and the reply is still sent. Owners: T9 `TestChatImages_ReRouteToTextOnlyDescribes`, `TestChatImages_NoVisionCannotSee`, `TestChatImages_VisionNotAllowedSensitive`, `TestChatImages_VisionFailsCannotSee`, `TestChatImages_TooLargeForImageProfile`.
3. **Lifecycle**:
   - An image is claimed atomically with its message; a claim of another chat's or an already-sent image fails the whole ingest.
   - Withdrawing a message deletes its images and only those.
   - Deleting a chat (and therefore deleting a bot, which deletes its chats first) deletes the chat's images, sent or not.
   - Unsent uploads go after 24 h; sent ones never do.

   Owners: T1 `TestChatImages_ClaimedByStoredMessage`, `TestChatImages_WithdrawAndDeleteRemoveThem`, `TestChatImages_PurgeOnlyUnsent`, `TestCleanup_PurgesUnsentChatImages`.
4. **Limits and API surface**: the 5th image, a duplicate ID, an ID of another chat, an already-sent ID or an unknown ID is refused (400) and nothing is stored. Image-only messages are accepted. GIF is 400 and over 8 MiB is 413. The image GET needs a session (401 without one) and returns only images of its own conversation (404 otherwise). It carries the panel CSP, `no-store` and `nosniff`. Uploads need CSRF. Owners: T6 `TestSubmitPanel_Images`; T5 `TestChatImagesAPI_Rejects`, `TestChatImagesAPI_AuthCSRFAndCSP`; T8 `TestChat_PostMessageBadImages`; T7 `TestChatPane_ImageComposer`.
5. **Turn assembly**:
   - In a merged multi-message turn, images keep message and attachment order, and only the newest 4 are kept; the dropped ones render `[图片]`.
   - Regenerate re-sends the same trigger messages' images.
   - The next turn sends no old image (history shows `[图片]`).
   - The state step is text-only.

   Owners: T4 `TestBuild_ChatImagesMergedOrder`; T6 `TestChatImages_MergedTurnKeepsNewestFour`, `TestChatImages_RegenerateKeepsImages`, `TestChatImages_HistoryNeverResent`, `TestChatImages_StateStepTextOnly`.

## Task order and parallel groups

| Stage | Tasks (parallel within a stage) | Starts after |
|---|---|---|
| A | T1 storage, T2 imaging copies, T3 ordered message parts | — |
| B | T4 context builder, T5 panel upload and serve | A merged (T4 needs T1 `Attachment.ID` + T3 `ChatPart`; T5 needs T1 + T2) |
| C | T6 core: submit with images, reply sees them; T7 panel web UI | B merged |
| D | T8 panel message API; T9 core: text-only fallback and text paths | T6 merged (T8 needs the variadic `SubmitPanel`; T9 edits `runner.go` and `chatimages.go` after T6) |
| E | T10 docs + full check | all merged |

Files per task (no two tasks of one stage share a file):

- T1: `pkg/conloomapi/types.go`, `internal/store/{records.go,store.go}`, `internal/store/sqlite/{chatimages.go (new),ingest.go,delete.go}`, `internal/store/sqlite/migrations/0023_chat_images.sql` (new), `internal/core/{cleanup.go,recover.go}`. Tests: `internal/store/sqlite/chatimages_test.go` (new), `internal/store/sqlite/delete_test.go` (`keyedTables` only), `internal/core/chat_images_cleanup_test.go` (new).
- T2: `internal/imaging/imaging.go`. Test: `internal/imaging/chat_test.go` (new).
- T3: `internal/provider/openai/{client.go,tools.go}`. Test: `internal/provider/openai/parts_test.go` (new).
- T4: `internal/contextbuilder/build.go`. Test: `internal/contextbuilder/chat_images_test.go` (new).
- T5: `internal/panel/{chatimages.go (new),images.go,write_routes.go,data_routes.go}`. Test: `internal/panel/chatimages_test.go` (new).
- T6: `internal/core/{panelchat.go,panelchat_errors.go,chatimages.go (new),runner.go,statestep.go}`. Test: `internal/core/chat_images_test.go` (new).
- T7: `internal/panel/web/views/{chat.js,chatclassic.js,chats.js}`, `internal/panel/web/lib/{api.js,format.js}`, `internal/panel/web/style.css`. Test: `internal/panel/web/chat_images_web_test.go` (new).
- T8: `internal/panel/{chat.go,messages.go,bots.go}`. Tests: `internal/panel/chat_test.go` (`fakeChat.SubmitPanel` only), `internal/panel/chat_images_msg_test.go` (new).
- T9: `internal/core/{runner.go,describe.go,chatimages.go,suggest.go,orchestrator.go}` (`orchestrator.go`: the `chatDescribeTimeout` field next to `describeTimeout` :161, :217). Test: `internal/core/chat_images_route_test.go` (new).
- T10: `docs/panel.md`, `docs/architecture.md`, `docs/configuration.md`.

---

### Task 1: Storage: `chat_images`, claim, delete hooks, purge

**Files:**
- Modify:
  - `pkg/conloomapi/types.go` (`Attachment` :134-140)
  - `internal/store/records.go` (after `PersonaImage` :165-180)
  - `internal/store/store.go` (after the persona image methods :187-201; docs of `DeleteConversation`/`DeleteMessagesFrom` :483-497)
  - `internal/store/sqlite/ingest.go` (`insertMessage` :135)
  - `internal/store/sqlite/delete.go` (`conversationTables` :18-24, `deleteMessagesFrom` :93-127)
  - `internal/core/cleanup.go` (consts :10-31, after `purgePersonaImages` :63-77)
  - `internal/core/recover.go` (:210-232)
- Create: `internal/store/sqlite/migrations/0023_chat_images.sql`, `internal/store/sqlite/chatimages.go` (pattern: `personaimages.go`)
- Test: `internal/store/sqlite/chatimages_test.go` (new; helpers `migrateOnlyAdding` autonomy_test.go:894, `countRows`), `delete_test.go` (`keyedTables` += `"chat_images"`), `internal/core/chat_images_cleanup_test.go` (new; pattern cleanup_test.go:~390 for `persona_images`)

**Interfaces:**
- `conloomapi.Attachment` gains `ID string \`json:"id,omitempty"\`` after `Type`. `const MaxPanelMessageImages = 4` (doc: the most images a panel chat message carries).
- Migration:
  ```sql
  -- Images sent in panel chats: a view copy (long edge <= 1536) and a reply copy (<= 768), both JPEG; no original.
  -- message_id is '' until the message carrying the image is stored, which claims it in the same transaction.
  CREATE TABLE chat_images (
      id TEXT PRIMARY KEY, conversation_key TEXT NOT NULL, message_id TEXT NOT NULL DEFAULT '',
      view_jpeg BLOB NOT NULL, view_width INTEGER NOT NULL, view_height INTEGER NOT NULL,
      reply_jpeg BLOB NOT NULL, reply_width INTEGER NOT NULL, reply_height INTEGER NOT NULL, reply_tokens INTEGER NOT NULL,
      description TEXT NOT NULL DEFAULT '',   -- cached vision description, '' when none
      created_by TEXT NOT NULL, created_at TEXT NOT NULL);
  CREATE INDEX chat_images_conversation_message ON chat_images (conversation_key, message_id);
  CREATE INDEX chat_images_unsent ON chat_images (created_at) WHERE message_id = '';
  ```
- `store`:
  ```go
  // ChatImage is an image uploaded to a panel chat (see imaging.ProcessChat). MessageID is "" until a stored message
  // carries it. Description is its cached vision description, "" when none.
  type ChatImage struct {
      ID, ConversationKey, MessageID          string
      ViewJPEG                                []byte
      ViewWidth, ViewHeight                   int
      ReplyJPEG                               []byte
      ReplyWidth, ReplyHeight, ReplyTokens    int
      Description, CreatedBy                  string
      CreatedAt                               time.Time
  }
  // ChatImageData selects the copies GetChatImages reads; 0 reads none.
  type ChatImageData uint8
  const (ChatImageReply ChatImageData = 1 << iota; ChatImageView)
  // Store:
  PutChatImage(ctx context.Context, img ChatImage) error
  // GetChatImages returns the images of convKey among ids by ID; unknown IDs and other chats' images are left out.
  GetChatImages(ctx context.Context, convKey string, ids []string, data ChatImageData) (map[string]ChatImage, error)
  // SetChatImageDescription caches text for the image; ErrNotFound when convKey has no such image.
  SetChatImageDescription(ctx context.Context, convKey, id, text string) error
  // PurgeChatImages deletes the images no stored message carries that were uploaded before createdBefore.
  PurgeChatImages(ctx context.Context, createdBefore time.Time) (int, error)
  ```
- `insertMessage`: for a message on `store.PlatformPanel` with image attachments that have an `ID`, run `UPDATE chat_images SET message_id = ? WHERE conversation_key = ? AND message_id = '' AND id IN (SELECT value FROM json_each(?))`. If fewer rows change than there are IDs, return `fmt.Errorf("ingest: message %s: %d of %d images not found unsent in %s", …)`, which rolls back the whole ingest. Other platforms are untouched.
- `deleteMessagesFrom`: before the messages are deleted, run `DELETE FROM chat_images WHERE conversation_key = ? AND message_id IN (SELECT id FROM messages WHERE conversation_key = ? AND seq >= ?)`. `conversationTables` gains `"chat_images"`, so `DeleteConversation` also removes the chat's unsent images.
- Core: `chatImageGrace = 24 * time.Hour`, cleanup label `cleanupChatImages = "chat_images"`, and `func (o *Orchestrator) purgeChatImages()` (same shape as `purgePersonaImages`, log `purged unsent chat images`), called next to `purgePersonaImages` at both places in `recover.go`.

- [ ] **Step 1: Write failing tests**
  - `TestMigration0023_OnlyAddsChatImages`: `added` == `[chat_images, chat_images_conversation_message, chat_images_unsent, sqlite_autoindex_chat_images_1]`.
  - `TestChatImages_PutGet`: put A and B (fake bytes `[]byte{1,2,3}` / `{4,5}`) in chat K1. `GetChatImages(K1, [A,B,X], 0)` returns A and B with metadata and description, and nil blobs. `ChatImageReply` returns only `ReplyJPEG`; `ChatImageView` returns only `ViewJPEG`; `K2` returns an empty map; nil ids return an empty map.
  - `TestChatImages_ClaimedByStoredMessage`: ingest a panel message in K1 with `[{image, A}, {image, B}]` → both rows have `MessageID == msg.ID`. A later panel message claiming A again fails with no message stored. A panel message in K2 claiming an image of K1 fails. A QQ message with an `id` attachment is stored and claims nothing.
  - `TestChatImages_WithdrawAndDeleteRemoveThem`: K1 has m1 (A), m2 (B), a bot reply m3, and unsent C.
    - An ingest with `DeleteMessages{FromSeq: m3.Seq}` (the regenerate path) keeps A and B.
    - `DeleteMessagesFrom(K1, m2.Seq)` leaves A and C.
    - `DeleteConversation(K1)` leaves 0 rows for K1.
    - After that, `DeletePanelBot` succeeds and `countRows("chat_images") == 0`.
  - `TestChatImages_PurgeOnlyUnsent`: unsent rows from 25 h and 1 h ago and a sent row from 25 h ago; `PurgeChatImages(now-24h)` returns 1 and only the old unsent row is gone.
  - `TestChatImages_Description`: after `SetChatImageDescription(K1, A, "一只橘猫")`, `Get` returns it; the same call with `K2` returns `ErrNotFound`.
  - `TestCleanup_PurgesUnsentChatImages` (core): an unsent image created 25 h before `env.now`, after a cleanup round, is gone and `conloom_cleanup_deleted_total{table="chat_images"} == 1`. A sent image of the same age stays.
- [ ] **Step 2: Run** `CGO_ENABLED=1 GOFLAGS=-mod=readonly go test -race -count=1 ./internal/store/... ./internal/core/ -run 'ChatImages|Migration0023|PurgesUnsentChatImages'` → FAIL.
- [ ] **Step 3: Implement** per Interfaces; update the store docs (`DeleteConversation` lists chat images; `DeleteMessagesFrom` deletes the images of the deleted messages).
- [ ] **Step 4: Run** `CGO_ENABLED=1 GOFLAGS=-mod=readonly go test -race -count=1 ./pkg/... ./internal/store/... ./internal/core/` → PASS.
- [ ] **Step 5: Commit** `feat(store): chat images table with claim, withdraw, delete and purge`

### Task 2: Imaging: view and reply copies

**Files:**
- Modify: `internal/imaging/imaging.go` (consts :21-27, after `Process` :51-74, package doc :1-3)
- Test: `internal/imaging/chat_test.go` (new)

**Interfaces:**
```go
// ViewEdge is the long edge of a chat image's view copy, shown when opened and described by the vision model.
const ViewEdge = 1536
// Chat is a checked chat image: a view copy (ViewEdge) and a reply copy (ReplyEdge), both JPEG. The original is not kept.
type Chat struct {
    ViewJPEG                             []byte
    ViewWidth, ViewHeight                int
    ReplyJPEG                            []byte
    ReplyWidth, ReplyHeight, ReplyTokens int
}
// ProcessChat checks data as Process does (sniffed PNG/JPEG, limits, flattened onto white), decodes it once and
// shrinks both copies from the decoded pixels.
func ProcessChat(data []byte) (Chat, error)
```

- [ ] **Step 1: Write failing tests** (all images generated in the test)
  - `TestProcessChat_Copies`: a 3000×1500 PNG whose top-left pixel is transparent gives a view copy of 1536×768 and a reply copy of 768×384. Both decode with `jpeg.Decode` at those sizes, `ReplyTokens == Tokens(768, 384)`, and the top-left pixel of the view copy is white (each channel ≥ 250).
  - `TestProcessChat_NeverEnlarges`: a 100×50 JPEG gives 100×50 for both copies.
  - `TestProcessChat_Rejects`: GIF bytes give `ErrUnsupported`; a 9000×10 PNG gives `ErrTooLarge`; `MaxBytes+1` bytes give `ErrTooLarge`.
- [ ] **Step 2: Run** `CGO_ENABLED=1 GOFLAGS=-mod=readonly go test -race -count=1 ./internal/imaging/ -run ProcessChat` → FAIL.
- [ ] **Step 3: Implement**; package doc: "checks uploaded persona and chat images and makes the smaller JPEG copies…".
- [ ] **Step 4: Run** `CGO_ENABLED=1 GOFLAGS=-mod=readonly go test -race -count=1 ./internal/imaging/` → PASS.
- [ ] **Step 5: Commit** `feat(imaging): view and reply copies for chat images`

### Task 3: Ordered message parts

**Files:**
- Modify: `internal/provider/openai/client.go` (`ChatMessage` :30-40), `internal/provider/openai/tools.go` (`MarshalJSON` :113-129, doc of `UnmarshalJSON` :131-132)
- Test: `internal/provider/openai/parts_test.go` (new)

**Interfaces:**
```go
// ChatPart is one part of a message sent as ordered parts: an image data URL (ImageURL) or else Text.
type ChatPart struct{ Text, ImageURL string }
// ChatMessage gains:
//   Parts, when set, is sent as the content in order (image_url and text parts; empty texts left out) instead of
//   Content and Images, which the sender may keep for estimates. Messages without Parts are sent as before.
Parts []ChatPart
```
`UnmarshalJSON` stays as it is (parts read back into `Content`/`Images`, not `Parts`).

- [ ] **Step 1: Write failing tests**
  - `TestChatMessage_PartsInOrder`: `{Role:"user", Content:"ignored", Images:["data:z"], Parts:[{Text:"A"},{ImageURL:"data:x"},{Text:""},{Text:"B"}]}` marshals exactly to `{"role":"user","content":[{"type":"text","text":"A"},{"type":"image_url","image_url":{"url":"data:x"}},{"type":"text","text":"B"}]}`.
  - `TestChatMessage_WithoutPartsUnchanged`: literal JSON for a user text message, an assistant message with only tool calls (`content: null`), a tool message, and a message with `Images` (image parts first, then the text), identical to today's output.
- [ ] **Step 2: Run** `CGO_ENABLED=1 GOFLAGS=-mod=readonly go test -race -count=1 ./internal/provider/openai/ -run ChatMessage_` → FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `CGO_ENABLED=1 GOFLAGS=-mod=readonly go test -race -count=1 ./internal/provider/...` → PASS.
- [ ] **Step 5: Commit** `feat(openai): ordered message parts`

### Task 4: Context builder: current-turn images, labels, placeholders

**Files:**
- Modify: `internal/contextbuilder/build.go` (`Input` :40-76, `AppearanceLabel`/`ImageRef` :78-87, `BaseTokens` :106-124, `Build` :139-224, `renderCurrent`/`renderBody`/`renderPlaceholders` :425-468, `WithoutImage` :479-491)
- Test: `internal/contextbuilder/chat_images_test.go` (new)

**Interfaces:**
```go
// ChatImage is an image the person sent with Current: ID is its attachment ID. With a DataURL the model sees it
// (Tokens is its estimate); without one, Description stands in for it, "" meaning the model cannot see it.
type ChatImage struct {
    ID, DataURL string
    Tokens      int
    Description string
}
func ChatImageLabel(n int) string // Global Constraints
// Input gains:
//   CurrentImages: images of Current in message and attachment order (the caller keeps at most four).
//   ImageDescriptions: cached descriptions by attachment ID, for image attachments not in CurrentImages.
CurrentImages     []ChatImage
ImageDescriptions map[string]string
```
- Rendering (current block and history): an image attachment whose ID is in `CurrentImages` renders `[图片N]` when seen. Otherwise it renders `[图片：<Description>]`, or `[图片（看不到内容）]` when there is no description; `imagesUnseenNote` follows once after the current bodies. Any other image attachment renders `[图片：<ImageDescriptions[id]>]` when there is a description, else `[图片]`. Descriptions are folded onto one line with `strings.Fields`. An image-only current message renders just its placeholders. `Transcript`/`renderCappedBody` (memory, search) keep `[图片]`.
- `Build`: with at least one seen image, the final user message keeps `Content` as the full text and gets `Parts`, in this order:
  1. `{Text: AppearanceLabel}` and `{ImageURL: Image.DataURL}`, only when the appearance image would go on this same message. That message then has no `Images`, and its `Content` has no `AppearanceLabel` prefix.
  2. For each seen image n: `{Text: ChatImageLabel(n)}` and `{ImageURL: DataURL}`.
  3. `{Text: Content}`.

  Otherwise the output is exactly today's.
- `BaseTokens` and `EstimatedTokens` add the seen images' `Tokens` and `Estimate(ChatImageLabel(n))`.
- `WithoutImage`: returns msgs with no image anywhere. Every message gets `Images` and `Parts` set to nil; the message that carried `Images` loses its `AppearanceLabel` line, and a message with `Parts` keeps its `Content`. msgs itself is left unchanged.

- [ ] **Step 1: Write failing tests** (fake data URLs `data:image/jpeg;base64,QQ==` etc.)
  - `TestBuild_NoChatImagesUnchanged`: an input with QQ-style image attachments (no ID) and an appearance image renders `[图片]` and the same `Messages`/`EstimatedTokens` with `CurrentImages`/`ImageDescriptions` nil as with empty values; no message has `Parts`.
  - `TestBuild_ChatImagesOnCurrentMessage`: history (user, assistant) plus current `看这个` with attachments A and B, both seen. The final message's Parts are `[L1, a, L2, b, {Content}]`. Its Content contains `看这个[图片1][图片2]` and no bare `[图片]`. History has no Parts or Images. `EstimatedTokens` and `BaseTokens` each grow by at least `tokA+tokB` over the same input without `CurrentImages`.
  - `TestBuild_ChatImagesWithAppearanceSameMessage`: no history, `Image` set, A seen. There is one user message: Parts `[AppearanceLabel, app, L1, a, {Content}]`, `Images` nil, Content without `AppearanceLabel`. Its marshalled JSON has two `image_url` parts, each right after its label text.
  - `TestBuild_ChatImagesWithAppearanceEarlierMessage`: with user history first, the first user message is as today (`AppearanceLabel\n…`, `Images [app]`), and the final message's Parts carry only the chat images.
  - `TestBuild_ImageOnlyMessage`: current text `""` with A seen gives the body `[图片1]` (not `（没有文字内容）`).
  - `TestBuild_ChatImagesMergedOrder`: m1 (A,B), m2 `还有` (C), m3 (D,E), with `CurrentImages` B,C,D,E and `ImageDescriptions{A:"一只橘猫"}`. Bodies are `[图片：一只橘猫][图片1]`, `还有[图片2]`, `[图片3][图片4]`; the Parts images are in order b,c,d,e with labels 1..4.
  - `TestBuild_UnseenImages`: A has `Description:"桌上的\n一杯咖啡"` and B has none. The body is `[图片：桌上的 一杯咖啡][图片（看不到内容）]`, `imagesUnseenNote` appears once before the directive, and there are no Parts.
  - `TestBuild_HistoryImagesUseCachedDescriptions`: history attachments X (described) and Y render `[图片：…][图片]`; no history message has Images or Parts.
  - `TestWithoutImage_StripsEveryImage`: appearance on the first message and chat images on the final one. The result has no Images and no Parts; the first Content lacks `AppearanceLabel`; the final Content is unchanged; the input slice is unchanged.
  - `TestTranscript_ChatImagesStayPlaceholders`: a message with ID'd attachments renders `[图片][图片]`.
- [ ] **Step 2: Run** `CGO_ENABLED=1 GOFLAGS=-mod=readonly go test -race -count=1 ./internal/contextbuilder/ -run 'ChatImages|ImageOnly|UnseenImages|HistoryImages|StripsEveryImage|ChatImagesStay'` → FAIL.
- [ ] **Step 3: Implement**; update the docs of `Input`, `Build`, `BaseTokens`, `WithoutImage`, `renderPlaceholders`.
- [ ] **Step 4: Run** `CGO_ENABLED=1 GOFLAGS=-mod=readonly go test -race -count=1 ./internal/contextbuilder/ ./internal/core/ -run 'Build|Transcript|Strip|ReplyImage'`, then the whole `./internal/contextbuilder/` → PASS (existing tests unmodified).
- [ ] **Step 5: Commit** `feat(contextbuilder): current-turn chat images with labels and placeholders`

### Task 5: Panel upload and serve

**Files:**
- Create: `internal/panel/chatimages.go`
- Modify: `internal/panel/images.go` (move the `ErrUnsupported`/`ErrTooLarge` mapping of `uploadImage` :64-78 into a helper), `internal/panel/write_routes.go` (+ route, doc), `internal/panel/data_routes.go` (+ route)
- Test: `internal/panel/chatimages_test.go` (new; store harness from `storeharness_test.go`, CSP constant `wantCSP` auth_test.go:671)

**Interfaces:**
```go
// imageRejected writes the 400 for an imaging.ErrUnsupported or ErrTooLarge as uploadImage does and reports whether it did.
func (s *Server) imageRejected(w http.ResponseWriter, r *http.Request, err error) bool
// uploadChatImage: POST /panel/api/conversations/{key}/images. The body is the raw PNG/JPEG bytes (Content-Type ignored),
// at most imaging.MaxBytes (413), processed by imaging.ProcessChat under the s.imageUploads slot (409 CHAT_BUSY).
// Only a private panel chat takes images (400 "只能在面板私聊里发图片"). It stores store.ChatImage{ID: ids.New("img"),
// ConversationKey, CreatedBy: panelActor(sess)}, audits panel.chat.image.upload (bot, conv key; image_id, width,
// height, bytes, reply_tokens, never pixels) and answers 201 {"id","width","height","reply_tokens"} (view copy size).
func (s *Server) uploadChatImage(w http.ResponseWriter, r *http.Request, sess *Session)
// getChatImage: GET /panel/api/conversations/{key}/images/{id}?size=reply|view: the reply copy (default) or the
// view copy as image/jpeg; another value 400; an image not of this conversation 404.
func (s *Server) getChatImage(w http.ResponseWriter, r *http.Request, _ *Session)
```

- [ ] **Step 1: Write failing tests** (PNG generated in the test; the panel chat is seeded as a `store.PlatformPanel` private conversation)
  - `TestChatImagesAPI_UploadAndGet`: uploading a 2000×1000 PNG gives 201 with an `id` that starts with `img_`, width 1536 and height 768. The stored row has the conversation key, `MessageID == ""` and `CreatedBy == "panel:admin"` (`panelActor`). The audit row is `panel.chat.image.upload`, its details have the five keys and no `base64`. `GET` returns `image/jpeg` decoding to 768×384, `?size=view` decodes to 1536×768, and `?size=original` is 400.
  - `TestChatImagesAPI_Rejects`:
    - Bodies: GIF → 400 containing `只支持 PNG 和 JPEG 图片`; 8 MiB + 1 → 413; a 9000×10 PNG → 400.
    - Conversations: a seeded QQ private conversation → 400 `只能在面板私聊里发图片`; an unknown key → 404.
    - Images: GET of an image of another panel chat → 404.
    - Busy: holding `s.imageUploads` → 409 `CHAT_BUSY`.
    - Nothing is stored on any refusal.
  - `TestChatImagesAPI_AuthCSRFAndCSP`: GET without a cookie → 401 and the body is not JPEG. POST without the CSRF header → 403. GET with a session carries `Content-Security-Policy == wantCSP`, `Cache-Control: no-store` and `X-Content-Type-Options: nosniff`.
  - If a route inventory test lists the API routes (e.g. `static_test.go`), add both routes there.
- [ ] **Step 2: Run** `CGO_ENABLED=1 GOFLAGS=-mod=readonly go test -race -count=1 ./internal/panel/ -run 'ChatImagesAPI'` → FAIL.
- [ ] **Step 3: Implement**; `uploadImage` uses `imageRejected` (its tests unmodified).
- [ ] **Step 4: Run** `CGO_ENABLED=1 GOFLAGS=-mod=readonly go test -race -count=1 ./internal/panel/...` → PASS.
- [ ] **Step 5: Commit** `feat(panel): upload and serve chat images`

### Task 6: Core: submit with images, the reply sees them

**Files:**
- Create: `internal/core/chatimages.go`
- Modify:
  - `internal/core/panelchat.go` (`SubmitPanel` :31-90)
  - `internal/core/panelchat_errors.go` (:13-17)
  - `internal/core/runner.go` (`execute`, after `in` is built :350-377)
  - `internal/core/statestep.go` (`stateInput` :525-552)
- Test: `internal/core/chat_images_test.go` (new; reuse `withImageModality`, `putTestImage`, `setAppearance`, `requestImages` from reply_image_test.go, `submitPanel` from panelchat_test.go, `stepEnv` from replystep_test.go:28)

**Interfaces:**
```go
var ErrBadImages = errors.New("…") // Global Constraints; answered with 400 like ErrBadText
// SubmitPanel … images are IDs of images uploaded to convKey and not yet sent, at most
// conloomapi.MaxPanelMessageImages and distinct, else ErrBadImages (checked under the chat's panel lock, nothing
// stored). With images the text may be blank (stored as ""); without, ErrBadText as before. They are stored as
// Attachment{Type: "image", ID: id, MIME: "image/jpeg"}, in order; the store claims them with the message.
func (o *Orchestrator) SubmitPanel(ctx context.Context, convKey, panelUser, text string, images ...string) (messageID, suppressed string, err error)
const maxTurnImages = 4
// turnImages returns, for a panel conversation, the reply copies of the image attachments of current (message
// order, the newest maxTurnImages) as seen images, and the cached descriptions of every other image attachment of
// history and current. An image that cannot be read is logged and left out (it renders [图片]). Other platforms: nil, nil.
func (o *Orchestrator) turnImages(ctx context.Context, task store.TaskRecord, conv store.ConversationRecord,
    history, current []store.MessageRecord) ([]contextbuilder.ChatImage, map[string]string)
```
- `execute` sets `in.CurrentImages, in.ImageDescriptions = o.turnImages(…)` before `BaseTokens`/routing, so image tokens count in the route. `stateInput` sets `in.CurrentImages = nil` (keeping `ImageDescriptions`), so the state step is text-only.

- [ ] **Step 1: Write failing tests.** Helpers:
  - `(e *testEnv) putChatImage(t, convKey string, w, h int) store.ChatImage`: a generated PNG run through `imaging.ProcessChat` and `PutChatImage` with `ids.New("img")`.
  - `chatImageParts(t, req map[string]any) (texts, urls []string)`: the final user message's parts.

  Tests:
  - `TestSubmitPanel_Images`:
    - An image-only submit stores `Text ""` with `Attachments [{image, A, image/jpeg}]`, and A is claimed.
    - Text plus 2 images is accepted.
    - Each of these gives `ErrBadImages` and stores nothing: 5 IDs, a duplicate, an ID of another chat, an already-sent ID, an unknown ID.
    - Blank text without images still gives `ErrBadText`.
    - A text-only submit still stores `Attachments: []`.
  - `TestChatImages_ReplySeesImages` (short has the image modality): submitting `你看` with A and B.
    - The reply request's final user parts are `[ChatImageLabel(1), "data:image/jpeg;base64,"+base64(A.ReplyJPEG), ChatImageLabel(2), …B, text]`, and the text contains `你看[图片1][图片2]`.
    - The recorded model call's estimated tokens are at least `A.ReplyTokens+B.ReplyTokens` above those of an identical text-only turn in a fresh env.
  - `TestChatImages_WithAppearanceInFirstMessage`: the chat persona has an appearance image and the turn is the first one. The single user message's parts start with `AppearanceLabel`, the appearance URL, then `ChatImageLabel(1)` and A.
  - `TestChatImages_HistoryNeverResent`: after the reply, a text-only message's request has no `image_url` (appearance off), and its history line contains `[图片][图片]`.
  - `TestChatImages_MergedTurnKeepsNewestFour`: m1 (A,B,C) and m2 (D,E) are merged into one task (debounce). The sent images are in order B,C,D,E. m1 renders `[图片][图片1][图片2]` and m2 `[图片3][图片4]`.
  - `TestChatImages_RegenerateKeepsImages`: after the reply, `RegeneratePanel` gives a new reply request carrying the same image URLs and labels.
  - `TestChatImages_StateStepTextOnly` (`stepEnv` with a state tool): the state step request has no `image_url` and its current body contains `[图片]`, not `[图片1]`.
- [ ] **Step 2: Run** `CGO_ENABLED=1 GOFLAGS=-mod=readonly go test -race -count=1 ./internal/core/ -run 'SubmitPanel_Images|ChatImages_'` → FAIL.
- [ ] **Step 3: Implement** per Interfaces; doc updates for `SubmitPanel`, `stateInput`, `execute`'s image comment.
- [ ] **Step 4: Run** `CGO_ENABLED=1 GOFLAGS=-mod=readonly go test -race -count=1 ./internal/core/ ./internal/app/` → PASS (`*core.Orchestrator` still satisfies `panel.ChatService`, since the variadic call is compatible).
- [ ] **Step 5: Commit** `feat(core): panel messages carry images the reply model sees`

### Task 7: Panel web UI

**Files:**
- Modify:
  - `internal/panel/web/views/chat.js` (`bubble` :95-102, `chatPane` composer :294-297 and :834-902, menu `copyItem` :342)
  - `internal/panel/web/views/chatclassic.js` (`bubble` :17-27)
  - `internal/panel/web/views/chats.js` (`preview` :785-791, search :825)
  - `internal/panel/web/lib/api.js` (next to `convPath` :63)
  - `internal/panel/web/lib/format.js`
  - `internal/panel/web/style.css`
- Test: `internal/panel/web/chat_images_web_test.go` (new; `assets(t, ".js")`, pattern `TestAssets_AppearanceLimitsMatchServer`)

**Interfaces (API contract from T5/T8):**
- `POST conversations/{key}/images` returns `{id,width,height,reply_tokens}`.
- `POST conversations/{key}/messages` takes `{text, images?}`.
- Message views and `chats[].last` carry `images: [id…]` (omitted when none).

**Implementation:**
- `lib/api.js`: `export function chatImageURL(key, id, size)` returns `api/conversations/<key>/images/<id>?size=<size>`, built like `convPath` and `avatar.js imageURL`.
- `lib/format.js`: `export function previewText(m)`, which gives `[图片]`, `[图片] <text>` or `<text>`.
- `chat.js` constants: `const MAX_IMAGES = 4;` (`conloomapi.MaxPanelMessageImages`), `const MAX_IMAGE_MIB = 8;` and `IMAGE_TYPES = ['image/png', 'image/jpeg']`.
- Composer:
  - Controls: a `tg-attach` button 📎 (`title`/`aria-label` `发送图片`) opens a hidden `input type=file accept: 'image/png,image/jpeg', multiple: true`. `paste` on the input adds the clipboard image files (and calls `preventDefault` only then). `dragover`/`drop` on the `tg-conv` section adds the dropped files, with a `tg-drop` class while dragging.
  - Refusals: a 5th image, other types and files over 8 MiB are refused in `sendError` with the fixed copy.
  - Previews: a `ul.tg-attachments` strip above the input. Each item shows `上传中…` until uploaded, then an `img` of the reply copy, plus a `×` button (`aria-label` `移除图片`).
  - Uploads: one at a time (the server holds one upload slot) via `upload(convPath(key, 'images'), file)`. An error shows `errorBox` and drops that item.
  - Send: possible with text or ready images, and not while an upload runs. The body is `{ text }` with `images: ids` only when there are images, so text-only bodies are unchanged. Success clears the strip; failure keeps it.
- Bubbles: `export function imageStrip(key, m)` returns a `div.tg-images` of `img.tg-image` (reply copy, `alt '图片'`, `loading 'lazy'`), or null. A click opens a `div.tg-viewer` overlay (`role dialog`, `aria-label '查看图片'`) with the view copy, which a click or Escape closes and which ends with the route `signal`. The text span is rendered only when `m.text`, and 复制 is hidden for messages without text. `chatclassic.js` imports `imageStrip` into its bubble (render only, no attach UI).
- `chats.js`: `preview` and the search filter use `previewText(c.last)`.
- `style.css`: `tg-` scoped rules for the attach button, the strip, the thumbnails (max 240×240, `object-fit: cover`, rounded, 2 columns), the viewer (fixed, max 95vw/95vh) and `tg-drop`. No API newer than Chrome 89.

- [ ] **Step 1: Write failing tests**
  - `TestChatPane_ImageComposer`: `chat.js` contains `const MAX_IMAGES = 4;` (equal to `conloomapi.MaxPanelMessageImages`), `const MAX_IMAGE_MIB = 8;` (equal to `imaging.MaxBytes >> 20`), `accept: 'image/png,image/jpeg'`, `'paste'`, `'drop'`, `convPath(key, 'images')`, `images: ids` and every fixed UI string.
  - `TestChatPane_ImageBubbles`: `chat.js` exports `imageStrip` using `chatImageURL(key, id, 'reply')` and `'view'` and contains `tg-viewer`, `'Escape'`; `chatclassic.js` imports and calls `imageStrip(`.
  - `TestChatsList_ImagePreview`: `format.js` has `previewText` with `'[图片]'`; `chats.js` uses `previewText(c.last)` at least twice.
  - `TestChatImages_StylesScoped`: `style.css` has `.tg-attachments`, `.tg-images`, `.tg-viewer`, `.tg-drop`.
- [ ] **Step 2: Run** `CGO_ENABLED=1 GOFLAGS=-mod=readonly go test -race -count=1 ./internal/panel/web/ -run 'Image'` → FAIL.
- [ ] **Step 3: Implement**; `node --check` each changed JS file.
- [ ] **Step 4: Run** `CGO_ENABLED=1 GOFLAGS=-mod=readonly go test -race -count=1 ./internal/panel/web/` → PASS (existing asset tests unmodified, including the Chrome 89, imports and styles-scoped tests).
- [ ] **Step 5: Commit** `feat(panel): attach and show images in panel chats`

### Task 8: Panel message API

**Files:**
- Modify: `internal/panel/chat.go` (`ChatService` :17-26, `chatFail` :103, `postChatMessage` :124-156), `internal/panel/messages.go` (`messageView` :16-35), `internal/panel/bots.go` (`chatLast` :104-121)
- Test: `internal/panel/chat_test.go` (`fakeChat.SubmitPanel` :96 takes `images ...string` and records them), `internal/panel/chat_images_msg_test.go` (new)

**Interfaces:**
- `ChatService.SubmitPanel(ctx context.Context, convKey, panelUser, text string, images ...string) (messageID, suppressed string, err error)`.
- `postChatMessage` decodes `{text, images []string}`. `chatFail`'s 400 list gains `core.ErrBadImages`. The audit adds `Images int \`json:"images,omitempty"\``.
- `messageView` and `chatLast` gain `Images []string \`json:"images,omitempty"\``: the IDs of the message's image attachments that have an ID, in order.

- [ ] **Step 1: Write failing tests**
  - `TestChat_PostMessageWithImages`: posting `{"text":"","images":["img_a","img_b"]}` gives 202, the fake records both IDs, and the audit has `images: 2`. A text-only post's audit details are unchanged (no `images` key).
  - `TestChat_PostMessageBadImages`: the fake returns `core.ErrBadImages`, giving 400 `INVALID_EVENT` with the message without `core: `.
  - `TestChat_PollShowsImages` (store harness): a seeded panel message with attachments `[{image, img_a}, {image, ""}]` shows `images == ["img_a"]` in `GET chat` and in the message list, and a text-only message has no `images` key.
  - `TestChats_LastShowsImages`: `GET chats` `last.images == ["img_a"]` when the newest message has an image.
- [ ] **Step 2: Run** `CGO_ENABLED=1 GOFLAGS=-mod=readonly go test -race -count=1 ./internal/panel/ -run 'Images'` → FAIL.
- [ ] **Step 3: Implement**; docs of `postChatMessage`, `messageView`, `chatLast`.
- [ ] **Step 4: Run** `CGO_ENABLED=1 GOFLAGS=-mod=readonly go test -race -count=1 ./internal/panel/... ./internal/app/` → PASS.
- [ ] **Step 5: Commit** `feat(panel): chat messages send and show image references`

### Task 9: Core: text-only fallback and text paths

**Files:**
- Modify:
  - `internal/core/runner.go` (`call` :697-728)
  - `internal/core/describe.go` (split the model call out of `DescribeAppearance` :54-117)
  - `internal/core/chatimages.go` (+ `blindImages`)
  - `internal/core/suggest.go` (`suggestTranscript` :251-273)
  - `internal/core/orchestrator.go` (`chatDescribeTimeout` field and its default)
- Test: `internal/core/chat_images_route_test.go` (new; T6 helpers; vision setup from describe_test.go)

**Interfaces:**
```go
const chatDescribePrompt = "…" // Global Constraints
const (chatDescribeTimeout = 30 * time.Second; chatDescriptionMaxRunes = 200)
// visionCall leases the vision profile p (admission.PriorityInteractive, ID leaseID) and sends prompt with one JPEG
// image; recorded with purpose vision, failed or not. DescribeAppearance and describeChatImage use it.
func (o *Orchestrator) visionCall(ctx context.Context, p *router.Profile, leaseID, prompt string, img []byte, w, h int) (string, error)
// blindImages returns in with every seen CurrentImages entry turned into text: the cached description, else one the
// vision profile writes from the view copy (cut to chatDescriptionMaxRunes, cached with SetChatImageDescription),
// else Description "" (cannot see). There is no description without a vision profile or, in a private chat, when the
// vision profile does not allow sensitive content. Descriptions share one chatDescribeTimeout; failures are not cached.
func (o *Orchestrator) blindImages(ctx context.Context, task store.TaskRecord, in contextbuilder.Input) contextbuilder.Input
```
- `call`: when `!slices.Contains(p.Config.Modalities, "image")`, run `in = o.blindImages(ctx, task, in)` before `Build`, so a re-route reuses the cache. `WithoutImage` still applies as before.
- `suggestTranscript`: after the text, append `[图片]` for each image attachment. A message with images but no text gives `name：[图片]…` and is no longer left out.

- [ ] **Step 1: Write failing tests** (vision fake model answers `一只橘猫趴在键盘上`; log captured to a buffer)
  - `TestChatImages_ReRouteToTextOnlyDescribes`: short has the image modality and fails, so the turn re-routes to long (text-only); vision is configured with `allow_sensitive_content`.
    - The vision request carries `chatDescribePrompt` and a 1536-edge view-copy data URL, and a `vision` usage row is recorded.
    - The long request has no `image_url`, and its current text contains `[图片：一只橘猫趴在键盘上]`. The row's `Description` is set.
    - The next turn's history shows `[图片：一只橘猫趴在键盘上]`. The log contains no `base64,`.
  - `TestChatImages_CachedDescriptionNotAskedAgain`: after the above, `RegeneratePanel` routes to long again, and vision is called exactly once in total.
  - `TestChatImages_NoVisionCannotSee`: there is no vision profile and short is text-only. The request text contains `[图片（看不到内容）]` and `imagesUnseenNote`, and the reply is sent.
  - `TestChatImages_VisionNotAllowedSensitive`: vision without `allow_sensitive_content` is never called, and the text shows the can't-see form.
  - `TestChatImages_VisionFailsCannotSee`: vision answers 500, or hangs past a short `chatDescribeTimeout` set on the orchestrator. The text shows the can't-see form, nothing is cached, and the reply is sent.
  - `TestChatImages_TooLargeForImageProfile`: short (image) has a window too small for text plus image tokens, and long (text-only) fits. The turn is routed to long, described, and carries no `image_url`.
  - `TestSuggestTranscript_Images`: `[{小明, "看", image}, {小明, "", image, image}]` gives `小明：看[图片]` and `小明：[图片][图片]`.
  - Existing `TestDescribe_*` and `TestReplyImage_*` pass unmodified.
- [ ] **Step 2: Run** `CGO_ENABLED=1 GOFLAGS=-mod=readonly go test -race -count=1 ./internal/core/ -run 'ChatImages_|SuggestTranscript_Images|Describe|ReplyImage'` → FAIL (new tests).
- [ ] **Step 3: Implement**; docs of `call` (text-only profiles get descriptions), `DescribeAppearance`, `suggestTranscript`.
- [ ] **Step 4: Run** `CGO_ENABLED=1 GOFLAGS=-mod=readonly go test -race -count=1 ./internal/core/` → PASS.
- [ ] **Step 5: Commit** `feat(core): describe chat images for text-only models`

### Task 10: Docs and full check

**Files:** `docs/panel.md`, `docs/architecture.md`, `docs/configuration.md`

- `panel.md`:
  - Composer: 📎, paste, drop, previews with ×, 1–4 PNG/JPEG ≤ 8 MiB, image-only messages, uploads one at a time.
  - Bubbles: thumbnails above the text; a click opens the 1536 view.
  - Chat list preview `[图片]`; the classic page renders images without the attach UI.
  - API rows: `POST conversations/{key}/images`, `GET conversations/{key}/images/{id}?size=reply|view`, `POST …/messages` `{text, images}` (400 for bad images), `images` in message views and `chats[].last`.
  - Audit `panel.chat.image.upload`; retention (withdraw, chat/bot deletion, unsent purged after 24 h).
- `architecture.md`:
  - The `chat_images` table and the claim on message insert.
  - Context: current-turn images (newest 4, labels, `[图片N]`, parts order with the appearance image); history and text paths use `[图片]` or `[图片：描述]` and never re-send.
  - Routing: images count in the estimate; a text-only profile gets vision descriptions (cached) or the can't-see placeholder and note.
  - The state step, suggestions, memory and search are text-only.
  - The `vision` role is now also used for chat images.
- `configuration.md`: `model_profiles.*.modalities: [text, image]` makes a reply profile see chat images. `vision` also describes chat images for text-only profiles and needs `privacy.allow_sensitive_content: true` for panel chats (private).

- [ ] **Step 1: Edit docs**; `grep -rn "\[图片\]\|persona-images\|vision" docs/*.md` and fix every hit that now misstates behaviour (leave `docs/superpowers/` alone).
- [ ] **Step 2: Full check:** `gofmt -l . ; go vet ./... ; CGO_ENABLED=1 GOFLAGS=-mod=readonly go test -race -count=1 ./...` → PASS; `/usr/local/node/bin/node --check` on `views/chat.js`, `views/chatclassic.js`, `views/chats.js`, `lib/api.js`, `lib/format.js`.
- [ ] **Step 3: Commit** `docs: panel chat images`

---

## Self-review

- **Spec coverage:**
  - Send (attach, paste, drop, previews, 1–4, PNG/JPEG ≤ 8 MiB, image-only) → T7, T6, T8.
  - Store (imaging, migration 0023, `Attachment.ID`) → T1, T2, T5.
  - Show (thumbnails, viewer, authenticated GET, CSP, list preview `[图片]`) → T5, T7, T8.
  - Model sees the images (current turn, all merged triggers, newest 4, labels, message order, history `[图片]`, token estimate) → T4, T6.
  - Can't see (vision description, cache, can't-see text, sensitivity) → T9.
  - Other paths: state step → T6; suggestions → T9; memory and recall unchanged → T4 test; regenerate → T6; withdraw, delete and purge → T1.
  - Audit → T5, T8. Docs → T10.
  - Decisions: 1 → T3/T4; 2 → T2/T6/T9; 3 → T1/T2/T5; 4 → T4; 5 → T1.
- **Names consistent:** `conloomapi.Attachment.ID`/`MaxPanelMessageImages`, `store.ChatImage`/`ChatImageData`/`ChatImageReply`/`ChatImageView` (T1) → T5/T6/T9; `imaging.ProcessChat`/`ViewEdge` (T2) → T5/T6; `openai.ChatPart`/`ChatMessage.Parts` (T3) → T4; `contextbuilder.ChatImage`/`ChatImageLabel`/`Input.CurrentImages`/`ImageDescriptions` (T4) → T6/T9; `core.ErrBadImages`, variadic `SubmitPanel`, `turnImages`, `putChatImage`, `chatImageParts` (T6) → T8/T9.

## Decisions on spec ambiguities

1. **Image IDs:** chat images get random IDs (`ids.New("img")`), not the content hash used for persona images, so the same photo sent twice has two independent lifecycles. Every read is scoped to its conversation.
2. **Link to the message:** the link is `chat_images.message_id`, set by SQLite inside the message's ingest transaction (panel messages only). `SubmitPanel` validates under the chat's panel lock first. "Unsent" means `message_id = ''`. An upload racing a chat deletion leaves at most an unsent row, which the 24 h purge removes.
3. **`core.md` stays unchanged:** it is part of every system prompt, so changing it would break byte-identity. Seen images are explained by their labels; unseen ones by the explicit `[图片（看不到内容）]` and `imagesUnseenNote`.
4. **Descriptions:** they are cached in `chat_images.description` and used in the reply context's history and in the state step (via `ImageDescriptions`). Memory consolidation, search chunks and suggestions keep plain `[图片]`.
5. **Where descriptions replace images:** in core before `Build`, per chosen profile (`call`), so the parts never reach a text-only model. `WithoutImage` is generalised as a safety net that removes all images and parts from every message. Routing is unchanged (`NeedsImage` stays unused): images never force or exclude a profile, they only add to `BaseTokens`.
6. **Parts messages:** a parts message keeps `Content` as its plain text, for estimates and `WithoutImage`; on the wire only `Parts` is sent. The appearance image moves into the parts only when it shares the final message with chat images.
7. **Upload sizes:** the upload response and `width`/`height` are the view copy's size. The bubble shows the reply copy scaled by CSS, and a click opens an in-page viewer with the view copy (not a new tab).
8. **Uploads in the composer:** they go one at a time because the server's single upload slot is shared with persona images; a 409 is shown as an error. The classic chat page renders images but has no attach UI.
9. **Blank text:** with images, whitespace-only text is stored as `""`. Image-only messages render only their placeholders.
10. **Preview text:** the chat list preview is built client-side from `images` (`previewText`) because arriving messages also update it. The `chats[].last.text` cut is unchanged.
11. **Vision time and priority:** describing is sequential within one 30 s budget per request, at interactive admission priority, recorded as `vision` usage. The vision call does not trip the reply breaker, as today.
12. **Not changed:** the conversation tab's message list (`views/conversation.js`) is not changed; it gets `images` in the API but still shows only text. Follow-up if wanted.
