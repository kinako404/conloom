# Images in panel chats (user → bot)

## Intent

In the panel's Telegram-style chat (`#/im`, also the classic chat page) the
admin can only type text. The user wants to send images to the bot and have
the bot actually see them, like sending a photo in Telegram. The reply model
(`short_context`, self-hosted qwen) already accepts images
(`modalities: [text, image]`); the fallback `long_context` is text-only; a
`vision` profile exists for describing images. Scope is the panel chat only —
Telegram/QQ stay as they are (they show `[图片]`).

## Goals

- **Send**: the chat composer gets an attach button (📎), paste from clipboard
  and drag-and-drop. Selected images show as small previews above the input,
  each removable, before sending. A message may carry 1–4 images, with or
  without text (image-only messages are allowed). PNG and JPEG only, at most
  8 MiB each (same rules as persona images); other files are refused with a
  clear message.
- **Store**: each uploaded image is processed with `internal/imaging` (sniffed
  PNG/JPEG, flattened, downscaled JPEG for the model, long edge ≤ 1536 like the
  vision path; plus a small thumbnail for the chat, long edge ≤ 512) and
  stored in a new table (migration 0023) keyed by an id, owned by the
  conversation. The message stores the image references in its existing
  `attachments` JSON (`Attachment{Type:"image", ...}` with the image id — add
  an `id` field to `conloomapi.Attachment` if needed, omitempty).
- **Show**: user messages with images render the thumbnails in the bubble (with
  the text below, TG-style); clicking opens the full image (served by an
  authenticated GET; CSP allows only `'self'` and `data:` images). Image-only
  messages show just the images. The chat list preview shows `[图片]` (plus
  text if any).
- **Model sees it**: for a reply, the images of the current turn's messages
  (all merged trigger messages, max 4 in total, newest kept) are attached to
  the current user message as image parts with a short label (distinct from
  the persona appearance label), in message order. Older messages in history
  stay text with `[图片]` placeholders — never re-sent. Image tokens count in
  the estimate (`imaging` token formula).
- **Model can't see images** (routed to a profile without `image` modality,
  e.g. re-route to the text-only fallback): before the request, describe each
  image with the `vision` profile (reuse the describe path, short prompt for a
  1–2 sentence factual description) and replace the image parts with text
  `[图片：<描述>]`; if no vision profile or describing fails, use
  `[图片（看不到内容）]` and the model is told it cannot see the image so it
  doesn't pretend. Descriptions may be cached per image id.
- **Other paths**: the state step, suggestions, memory consolidation and
  recall use text only (`[图片]` placeholders; a cached description may be
  used in the text where available — builder's call). Regenerate re-uses the
  same trigger messages, so images are included again. Withdraw and chat
  deletion delete the images of the deleted messages; retention purges follow
  the messages.
- **Privacy**: panel chats are private; images follow the same sensitive
  content gate as text (routing requires `allow_sensitive_content`; the vision
  profile must also allow it, otherwise fall back to the "can't see" text).
  Uploads are audited like persona image uploads (no image content in logs).

## Non-goals

- Telegram/QQ image support; stickers; GIF/WebP; video/files.
- The bot sending images.
- Image moderation beyond the existing text review.

## Anti-goals

- The bot pretending to see an image it didn't receive.
- Re-sending old images every turn (token blow-up).
- Breaking text-only chats or the persona appearance image path.

## Approaches

- **A. Upload first, then send references (chosen).** `POST` the image
  (raw body, like persona images) → id; the message `POST` carries
  `{text, images:[id…]}`. Reuses the existing upload/processing code, keeps
  the message send small, previews are real server-processed thumbnails.
- B. Multipart message send with images inline. One request, but duplicates
  image handling into the message path and complicates retries.
- C. Client-side base64 inside the message JSON. Simple, but large JSON bodies
  and no server-side processing/limits until send.

## Constraints

- Public repo: fake data only (tests use generated images); English commits.
- Text-only messages and existing chats behave exactly as before (requests
  byte-identical when no images).
- Reuse `internal/imaging` (limits 8 MiB, 8000 px edge, 25 Mpx), the persona
  image upload handler patterns (raw body, 413/400 errors, CSRF, audit), and
  `ChatMessage.Images` / `image_url` data URLs in the OpenAI client.
- `WithoutImage` must handle more than one image-bearing message (today it
  strips only the first, assuming the appearance label).
- Docs: panel.md (composer, API), architecture.md (images in context, routing
  fallback), configuration.md (vision use) as applicable.

## Left to the builder

Table/column names, exact Chinese labels, the thumbnail size, how the vision
description is cached, the composer layout details, and whether the classic
chat page gets the attach UI (it should at least render images).
