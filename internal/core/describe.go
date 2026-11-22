package core

import (
	"bytes"
	"cmp"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"image/jpeg"
	"strings"
	"time"

	"github.com/kinako404/conloom/internal/admission"
	"github.com/kinako404/conloom/internal/contextbuilder"
	"github.com/kinako404/conloom/internal/imaging"
	"github.com/kinako404/conloom/internal/provider/openai"
	"github.com/kinako404/conloom/internal/router"
	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/internal/usage"
)

// ErrNoVision reports that no vision model profile is configured.
var ErrNoVision = errors.New("core: no vision model configured")

const (
	// describeEdge is the most pixels on the long edge of the copy of an
	// appearance image sent to the vision model.
	describeEdge = 1536
	// describeTimeout bounds a whole appearance description.
	describeTimeout = 40 * time.Second
)

// describePrompt asks the vision model for the appearance part of a persona.
const describePrompt = `这是一个角色的外观参考图。请根据图片写一段 150–400 字的外貌描述，要能直接放进这个角色人设的"外貌"部分。

要求：
- 只写看得见的外观：发型和发色、眼睛、体型和身高给人的印象、服装、配饰、标志性特征、整体气质。
- 不写图里的文字、画师或作者信息、水印，也不写背景和场景。
- 用第三人称（她、他或这个角色），写成一整段连贯的文字，不加标题，不用列表或分点。
- 看不清或拿不准的细节就不写，不要编造。
- 只输出这段描述本身，不要开场白或说明。`

// DescribeAppearance asks the vision model to describe the appearance shown
// by the persona image imageID, as text for a persona's appearance that the
// caller reviews before saving; actor, who asked, is only logged. The
// original image, scaled to at most describeEdge pixels on its long edge as a
// JPEG, goes with describePrompt to the vision profile (see visionCall). The
// whole description takes at most describeTimeout; hitting it is an error
// wrapping context.DeadlineExceeded. It returns the model's final answer,
// trimmed, without its reasoning; ErrNoVision when no vision profile is
// configured and store.ErrNotFound for an unknown image.
func (o *Orchestrator) DescribeAppearance(ctx context.Context, imageID, actor string) (string, error) {
	p, ok := o.router.Profile(router.RoleVision)
	if !ok {
		return "", ErrNoVision
	}
	ctx, cancel := context.WithTimeout(ctx, o.describeTimeout)
	defer cancel()
	img, err := o.st.GetPersonaImage(ctx, imageID, true)
	if err != nil {
		return "", err
	}
	scaled, err := imaging.Scaled(img.Original, describeEdge)
	if err != nil {
		return "", fmt.Errorf("core: scale appearance image %s: %w", imageID, err)
	}
	size, err := jpeg.DecodeConfig(bytes.NewReader(scaled))
	if err != nil {
		return "", fmt.Errorf("core: read scaled appearance image %s: %w", imageID, err)
	}

	start := time.Now()
	text, err := o.visionCall(ctx, p, "describe:"+imageID, describePrompt, scaled, size.Width, size.Height)
	latency := time.Since(start)
	if err != nil {
		o.log.Warn("describe appearance failed", "image", imageID, "actor", actor, "role", p.Role,
			"latency_ms", latency.Milliseconds(), "error", err)
		return "", err
	}
	o.log.Info("appearance described", "image", imageID, "actor", actor, "role", p.Role,
		"width", size.Width, "height", size.Height, "latency_ms", latency.Milliseconds())
	return text, nil
}

// visionCall sends prompt with the w×h JPEG img in a single user message to
// the vision profile p, once a lease of it (admission.PriorityInteractive, ID
// leaseID, lasting until ctx's deadline) is granted. The call is recorded
// with purpose vision, failed or not, and a successful one is logged (model
// call, without the image). It returns the model's final answer, trimmed,
// without its reasoning.
func (o *Orchestrator) visionCall(ctx context.Context, p *router.Profile, leaseID, prompt string, img []byte, w, h int) (string, error) {
	role, limits := string(p.Role), p.Config.Context
	estimate := imaging.Tokens(w, h) + contextbuilder.Estimate(prompt, p.Config.TokenEstimateFactor)
	ttl := o.describeTimeout
	if deadline, ok := ctx.Deadline(); ok {
		ttl = time.Until(deadline)
	}
	lease, err := o.adm.Acquire(ctx, admission.Request{
		ID:       leaseID,
		Profile:  role,
		Tokens:   estimate + limits.ReservedOutputTokens + limits.SafetyMarginTokens,
		Priority: admission.PriorityInteractive,
		TTL:      ttl + leaseSlack,
	})
	if err != nil {
		return "", fmt.Errorf("admission %s: %w", role, err)
	}
	defer lease.Release()

	req := openai.ChatRequest{
		Messages: []openai.ChatMessage{{
			Role: "user", Content: prompt,
			Images: []string{"data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(img)},
		}},
		MaxTokens: limits.ReservedOutputTokens,
	}
	start := time.Now()
	resp, err := p.Client.Chat(ctx, req)
	latency := time.Since(start)
	o.usage.Record(store.ModelCall{
		At: start.UTC(), Purpose: store.PurposeVision, Role: role, Model: cmp.Or(resp.Model, p.Config.Model),
		PromptTokens: resp.PromptTokens, CompletionTokens: resp.CompletionTokens, ReasoningTokens: resp.ReasoningTokens,
		LatencyMS: latency.Milliseconds(), Outcome: usage.Outcome(err), FinishReason: resp.FinishReason,
	})
	if err != nil {
		return "", fmt.Errorf("model %s: %w", role, err)
	}
	o.metrics.ModelLatency.WithLabelValues(role, resp.Model).Observe(latency.Seconds())
	o.metrics.ModelTokens.WithLabelValues(role, resp.Model, "prompt").Add(float64(resp.PromptTokens))
	o.metrics.ModelTokens.WithLabelValues(role, resp.Model, "completion").Add(float64(resp.CompletionTokens))
	o.metrics.ModelTokens.WithLabelValues(role, resp.Model, "reasoning").Add(float64(resp.ReasoningTokens))
	o.log.Info("model call", "purpose", store.PurposeVision, "role", role, "model", resp.Model,
		"latency_ms", latency.Milliseconds(), "estimated_tokens", estimate, "prompt_tokens", resp.PromptTokens,
		"completion_tokens", resp.CompletionTokens, "reasoning_tokens", resp.ReasoningTokens,
		"finish_reason", resp.FinishReason)
	return strings.TrimSpace(resp.Content), nil
}
