package core

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"github.com/kinako404/conloom/internal/botdir"
	"github.com/kinako404/conloom/internal/config"
	"github.com/kinako404/conloom/internal/ids"
	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

const (
	// maxPanelBots is the most panel bots there may be.
	maxPanelBots = 50
	// panelBotNameMax is the most characters of a panel bot's name.
	panelBotNameMax = 32
	// panelBotIDChars and panelBotIDLen make the part of a panel bot's ID
	// after config.PanelBotIDPrefix; panelBotIDAttempts is how many candidate
	// IDs Create tries before it gives up.
	panelBotIDChars    = "abcdefghijklmnopqrstuvwxyz0123456789"
	panelBotIDLen      = 8
	panelBotIDAttempts = 10
	// defaultPersonaTemplate is the persona template of a panel bot that names
	// none, and the one a bot whose template does not exist uses instead.
	defaultPersonaTemplate = "default"
)

// Errors of PanelBotService. The panel API answers ErrInvalidBot, which is
// wrapped with the reason in Chinese, with 400, ErrConfigBot with 403 and
// ErrBotDeleting with 409.
var (
	ErrBotDeleting = errors.New("core: bot is being deleted")
	ErrConfigBot   = errors.New("core: bot is defined in the config file")
	ErrInvalidBot  = errors.New("core: invalid bot")
)

// errPanelBotPlatform refuses an event of a panel bot that is not on the
// panel platform: no adapter speaks for a panel bot.
var errPanelBotPlatform = errors.New("core: a panel bot only takes panel events")

// PanelBotInput is what the panel sets of a panel bot: its name, the persona
// template its new chats start from ("default" when empty) and its skills.
type PanelBotInput struct {
	Name, PersonaTemplate string
	Skills                []string
}

// ConfigBotInput is what the panel sets of a config bot: its settings, as
// store.BotSettings has them. Empty persona templates are "default".
type ConfigBotInput struct {
	Name                           string
	Admins                         []string
	PrivateChat                    string
	PrivateAllowlist, Groups       []string
	PrivateTemplate, GroupTemplate string
	Skills                         []string
}

// SkillInfo is a skill a bot may enable, with the conversation types it
// applies to.
type SkillInfo struct {
	Name, Description string
	Conversations     []string
}

// PanelBotService creates, changes and deletes the panel bots, which are
// stored and published to the bot directory, where every component finds
// them.
type PanelBotService struct {
	o *Orchestrator
	// newID returns a candidate ID for a new bot: randomPanelBotID except in
	// tests.
	newID func() string

	// writeMu serialises Load, Create, Update and the store part of Delete,
	// so each publishes the panel bots it read from the store with its own
	// change and no other change is lost.
	writeMu sync.Mutex

	// mu guards deleting, the bots Delete is deleting.
	mu       sync.Mutex
	deleting map[string]bool
	// chatGates hold off CreatePanelChat of a bot while Delete marks it as
	// being deleted, so no chat is created after Delete lists the bot's
	// chats.
	chatGates lockTable
}

func newPanelBotService(o *Orchestrator) *PanelBotService {
	return &PanelBotService{o: o, newID: randomPanelBotID, deleting: map[string]bool{}}
}

// PanelBots returns the service over the orchestrator's panel bots.
func (o *Orchestrator) PanelBots() *PanelBotService { return o.panelBots }

// randomPanelBotID is config.PanelBotIDPrefix and panelBotIDLen random
// lowercase letters and digits.
func randomPanelBotID() string {
	b := make([]byte, panelBotIDLen)
	for i := range b {
		b[i] = panelBotIDChars[rand.IntN(len(panelBotIDChars))]
	}
	return config.PanelBotIDPrefix + string(b)
}

// Load publishes the config bots, each with its stored settings, and the
// stored panel bots to the bot directory, each as it takes effect with the
// persona templates of the moment, so call it again after a template change;
// an import of templates calls it itself (New sets it as the personas'
// OnImport). A config bot without stored settings takes effect as given to
// the directory. A persona template that does not exist is replaced by
// "default", and a skill that is no longer registered, no longer has its
// tools or, for a panel bot, is no longer usable in private chats is left
// out; each is logged as a warning and noted among the bot's problems in the
// directory. The config and the stored settings and bots are left as they
// are.
func (s *PanelBotService) Load(ctx context.Context) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	bots, err := s.o.st.ListPanelBots(ctx)
	if err != nil {
		return fmt.Errorf("core: load panel bots: %w", err)
	}
	settings, err := s.o.st.ListBotSettings(ctx)
	if err != nil {
		return fmt.Errorf("core: load bot settings: %w", err)
	}
	s.publishConfig(settings, true)
	s.publish(bots, true)
	s.o.log.Info("panel bots loaded", "count", len(bots))
	return nil
}

// Create stores a new panel bot from in for actor, under a new ID that no bot
// has had, publishes it and audits it as panel.bot.create; a failed audit is
// only logged. It returns the bot as the directory has it. Invalid input, and
// a 51st bot, is ErrInvalidBot.
func (s *PanelBotService) Create(ctx context.Context, in PanelBotInput, actor string) (config.BotConfig, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	in, err := s.validate(in)
	if err != nil {
		return config.BotConfig{}, err
	}
	bots, err := s.o.st.ListPanelBots(ctx)
	if err != nil {
		return config.BotConfig{}, err
	}
	if len(bots) >= maxPanelBots {
		return config.BotConfig{}, fmt.Errorf("%w: 面板 bot 最多 %d 个", ErrInvalidBot, maxPanelBots)
	}
	id, err := s.freeID(ctx)
	if err != nil {
		return config.BotConfig{}, err
	}
	now := s.o.now().UTC()
	b := store.PanelBot{ID: id, Name: in.Name, PersonaTemplate: in.PersonaTemplate, Skills: in.Skills,
		CreatedBy: actor, CreatedAt: now, UpdatedAt: now}
	if err := s.o.st.CreatePanelBot(ctx, b); err != nil {
		return config.BotConfig{}, err
	}
	s.publish(append(bots, b), false)
	s.o.log.Info("panel bot created", "bot", id)
	s.audit(ctx, id, "panel.bot.create", actor, auditDetail(in))
	return botdir.PanelBotConfig(b), nil
}

// Update replaces the name, persona template and skills of the panel bot id
// with in for actor, publishes it and audits it as panel.bot.update; a failed
// audit is only logged. The change applies from the bot's next reply; the new
// persona template only to chats without a persona yet. It returns the bot as
// the directory has it. A config bot, whose settings UpdateConfig changes, is
// ErrConfigBot, a bot being deleted ErrBotDeleting, an unknown one
// store.ErrNotFound and invalid input ErrInvalidBot.
func (s *PanelBotService) Update(ctx context.Context, id string, in PanelBotInput, actor string) (config.BotConfig, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if err := s.panelOnly(id); err != nil {
		return config.BotConfig{}, err
	}
	if err := s.notDeleting(id); err != nil {
		return config.BotConfig{}, err
	}
	bots, err := s.o.st.ListPanelBots(ctx)
	if err != nil {
		return config.BotConfig{}, err
	}
	i := slices.IndexFunc(bots, func(b store.PanelBot) bool { return b.ID == id })
	if i < 0 {
		return config.BotConfig{}, fmt.Errorf("%w: panel bot %q", store.ErrNotFound, id)
	}
	in, err = s.validate(in)
	if err != nil {
		return config.BotConfig{}, err
	}
	b := bots[i]
	b.Name, b.PersonaTemplate, b.Skills, b.UpdatedAt = in.Name, in.PersonaTemplate, in.Skills, s.o.now().UTC()
	if err := s.o.st.UpdatePanelBot(ctx, b); err != nil {
		return config.BotConfig{}, err
	}
	bots[i] = b
	s.publish(bots, false)
	s.o.log.Info("panel bot updated", "bot", id)
	s.audit(ctx, id, "panel.bot.update", actor, auditDetail(in))
	return botdir.PanelBotConfig(b), nil
}

// UpdateConfig replaces the settings of the config bot id with in for actor:
// it stores them, with the name of the bot's instance, publishes the config
// bots and audits it as panel.bot.update (see configAuditDetail); a failed
// audit is only logged. The core goes by the new settings at once: the
// groups, admins and private range from the next message, the name and
// skills from the next reply and the persona templates for conversations
// without a persona yet. The adapters read them from the capabilities. It
// returns the bot as the directory has it. A bot that is not a config bot is
// store.ErrNotFound and invalid input ErrInvalidBot.
func (s *PanelBotService) UpdateConfig(ctx context.Context, id string, in ConfigBotInput, actor string) (config.BotConfig, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	configBots := s.o.bots.ConfigBots()
	i := slices.IndexFunc(configBots, func(b config.BotConfig) bool { return b.ID == id })
	if i < 0 {
		return config.BotConfig{}, fmt.Errorf("%w: config bot %q", store.ErrNotFound, id)
	}
	in, err := s.validateConfig(in)
	if err != nil {
		return config.BotConfig{}, err
	}
	rows, err := s.o.st.ListBotSettings(ctx)
	if err != nil {
		return config.BotConfig{}, err
	}
	after := store.BotSettings{BotInstanceID: id, Name: in.Name, Admins: in.Admins, PrivateChat: in.PrivateChat,
		PrivateAllowlist: in.PrivateAllowlist, Groups: in.Groups, PrivateTemplate: in.PrivateTemplate,
		GroupTemplate: in.GroupTemplate, Skills: in.Skills, UpdatedBy: actor, UpdatedAt: s.o.now().UTC()}
	before := botdir.Settings(configBots[i])
	if j := slices.IndexFunc(rows, func(r store.BotSettings) bool { return r.BotInstanceID == id }); j >= 0 {
		before = rows[j]
		rows[j] = after
	} else {
		rows = append(rows, after)
	}
	if err := s.o.st.PutBotSettings(ctx, after); err != nil {
		return config.BotConfig{}, err
	}
	s.publishConfig(rows, false)
	s.o.log.Info("config bot's settings updated", "bot", id)
	s.audit(ctx, id, "panel.bot.update", actor, configAuditDetail(before, after))
	b, _ := s.o.bots.Bot(id)
	return b, nil
}

// Delete deletes the panel bot id for actor once confirmName, trimmed, is its
// name. It marks the bot as being deleted, which refuses new chats, messages
// in its chats and changes with ErrBotDeleting; deletes the bot's chats one
// by one (DeletePanelChat); deletes the memory its chats shared
// (store.Store.DeleteScope of store.PanelScopeKey); and then deletes the bot
// with what is left under its ID, keeping its bot instance, model calls and
// audit events (see store.Store.DeletePanelBot), removes it from the
// directory and audits it as panel.bot.delete; a failed audit is only
// logged. If a chat's tasks do not stop in time it stops there with
// ErrChatBusy: chats already deleted stay deleted, the bot stays and is no
// longer marked, so a retry can finish. A config bot is ErrConfigBot, a bot
// already being deleted ErrBotDeleting, an unknown one store.ErrNotFound and
// the wrong name ErrInvalidBot.
func (s *PanelBotService) Delete(ctx context.Context, id, confirmName, actor string) error {
	name, err := s.beginDelete(ctx, id, confirmName)
	if err != nil {
		return err
	}
	defer s.endDelete(id)
	chats, err := s.deleteChats(ctx, id)
	if err != nil {
		return err
	}
	err = s.o.st.DeleteScope(ctx, store.PanelScopeKey(id))
	if errors.Is(err, store.ErrBotHasData) {
		return fmt.Errorf("%w: %w", ErrChatBusy, err)
	}
	if err != nil {
		return err
	}
	if err := s.finishDelete(ctx, id); err != nil {
		return err
	}
	s.o.log.Info("panel bot deleted", "bot", id, "chats", chats)
	s.audit(ctx, id, "panel.bot.delete", actor, struct {
		Name  string `json:"name"`
		Chats int    `json:"chats"`
	}{name, chats})
	return nil
}

// beginDelete checks a Delete of id confirmed as confirmName and marks the
// bot as being deleted; it returns the bot's name.
func (s *PanelBotService) beginDelete(ctx context.Context, id, confirmName string) (string, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if err := s.panelOnly(id); err != nil {
		return "", err
	}
	bots, err := s.o.st.ListPanelBots(ctx)
	if err != nil {
		return "", err
	}
	i := slices.IndexFunc(bots, func(b store.PanelBot) bool { return b.ID == id })
	if i < 0 {
		return "", fmt.Errorf("%w: panel bot %q", store.ErrNotFound, id)
	}
	if err := s.notDeleting(id); err != nil {
		return "", err
	}
	if strings.TrimSpace(confirmName) != bots[i].Name {
		return "", fmt.Errorf("%w: 确认的名字和 bot 的名字不一致", ErrInvalidBot)
	}
	unlock := s.chatGates.lock(id)
	defer unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deleting[id] = true
	return bots[i].Name, nil
}

// endDelete ends the mark beginDelete set.
func (s *PanelBotService) endDelete(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.deleting, id)
}

// deleteChats deletes every conversation of bot id and returns how many it
// deleted; one deleted meanwhile counts as deleted.
func (s *PanelBotService) deleteChats(ctx context.Context, id string) (int, error) {
	n := 0
	for _, convType := range []string{conloomapi.ConvPrivate, conloomapi.ConvGroup} {
		convs, err := s.o.st.ListConversations(ctx, convType)
		if err != nil {
			return n, err
		}
		for _, c := range convs {
			if c.BotInstanceID != id {
				continue
			}
			if err := s.o.DeletePanelChat(ctx, c.Key); err != nil && !errors.Is(err, store.ErrNotFound) {
				return n, err
			}
			n++
		}
	}
	return n, nil
}

// finishDelete deletes the stored bot id, whose chats are gone, and removes
// it from the directory. Anything stored under it meanwhile is ErrChatBusy,
// with nothing deleted.
func (s *PanelBotService) finishDelete(ctx context.Context, id string) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	bots, err := s.o.st.ListPanelBots(ctx)
	if err != nil {
		return err
	}
	err = s.o.st.DeletePanelBot(ctx, id)
	if errors.Is(err, store.ErrBotHasData) {
		return fmt.Errorf("%w: %w", ErrChatBusy, err)
	}
	if err != nil {
		return err
	}
	s.publish(slices.DeleteFunc(bots, func(b store.PanelBot) bool { return b.ID == id }), false)
	return nil
}

// notDeleting returns ErrBotDeleting while bot id is being deleted.
func (s *PanelBotService) notDeleting(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.deleting[id] {
		return ErrBotDeleting
	}
	return nil
}

// openChat holds off a Delete of bot id from marking it until the returned
// unlock, so a chat created meanwhile is among the chats Delete deletes. A
// bot already being deleted is ErrBotDeleting.
func (s *PanelBotService) openChat(id string) (unlock func(), err error) {
	unlock = s.chatGates.lock(id)
	if err := s.notDeleting(id); err != nil {
		unlock()
		return nil, err
	}
	return unlock, nil
}

// panelOnly refuses a config bot with ErrConfigBot.
func (s *PanelBotService) panelOnly(id string) error {
	if b, ok := s.o.bots.Bot(id); ok && b.Platform != store.PlatformPanel {
		return fmt.Errorf("%w: %q", ErrConfigBot, id)
	}
	return nil
}

// freeID returns a candidate ID (newID) that is a valid bot ID
// (config.ValidBotID) and that no bot instance, also of a deleted panel bot,
// and no bot in the directory has.
func (s *PanelBotService) freeID(ctx context.Context) (string, error) {
	insts, err := s.o.st.ListBotInstances(ctx)
	if err != nil {
		return "", err
	}
	for range panelBotIDAttempts {
		id := s.newID()
		if !config.ValidBotID(id) {
			continue
		}
		_, inDirectory := s.o.bots.Bot(id)
		if !inDirectory && !slices.ContainsFunc(insts, func(b store.BotInstance) bool { return b.ID == id }) {
			return id, nil
		}
	}
	return "", fmt.Errorf("core: no free panel bot ID after %d attempts", panelBotIDAttempts)
}

// botName returns name trimmed when it may name a bot: 1 to panelBotNameMax
// characters without control, line separator or invisible format
// characters. Otherwise it is ErrInvalidBot with the reason in Chinese.
func botName(name string) (string, error) {
	name = strings.TrimSpace(name)
	var reason string
	switch {
	case name == "":
		reason = "名字不能为空"
	case utf8.RuneCountInString(name) > panelBotNameMax:
		reason = fmt.Sprintf("名字最多 %d 个字", panelBotNameMax)
	case !utf8.ValidString(name) || strings.ContainsFunc(name, func(r rune) bool {
		return unicode.IsControl(r) || r == '\u2028' || r == '\u2029'
	}):
		reason = "名字不能包含控制字符或换行"
	case strings.ContainsFunc(name, func(r rune) bool { return unicode.Is(unicode.Cf, r) }):
		reason = "名字不能包含零宽字符、方向控制符等不可见的格式字符"
	default:
		return name, nil
	}
	return "", fmt.Errorf("%w: %s", ErrInvalidBot, reason)
}

// validate returns in as it is stored: the name trimmed, the persona
// template "default" when empty and the skills without repeats. Invalid
// input is ErrInvalidBot with the reason in Chinese.
func (s *PanelBotService) validate(in PanelBotInput) (PanelBotInput, error) {
	invalid := func(format string, args ...any) (PanelBotInput, error) {
		return PanelBotInput{}, fmt.Errorf("%w: %s", ErrInvalidBot, fmt.Sprintf(format, args...))
	}
	name, err := botName(in.Name)
	if err != nil {
		return PanelBotInput{}, err
	}
	out := PanelBotInput{Name: name, PersonaTemplate: strings.TrimSpace(in.PersonaTemplate), Skills: []string{}}
	if out.PersonaTemplate == "" {
		out.PersonaTemplate = defaultPersonaTemplate
	}
	if _, ok := s.o.personas.Template(out.PersonaTemplate); !ok {
		return invalid("人设模板“%s”不存在", out.PersonaTemplate)
	}
	for _, name := range in.Skills {
		if slices.Contains(out.Skills, name) {
			continue
		}
		if problem := s.skillProblem(name); problem != "" {
			return invalid("%s", problem)
		}
		out.Skills = append(out.Skills, name)
	}
	return out, nil
}

// validateConfig returns in as it is stored: the name trimmed, the platform
// IDs and skills without repeats and the persona templates "default" when
// empty. Each platform ID must be non-empty, without whitespace, ':' or other
// invisible characters, the private range one of config.PrivateChatModes,
// the persona templates must exist and the skills be registered with all
// their tools; a skill only for groups is fine, the core leaves it out of
// private chats. Invalid input is ErrInvalidBot with the reason in Chinese.
func (s *PanelBotService) validateConfig(in ConfigBotInput) (ConfigBotInput, error) {
	invalid := func(format string, args ...any) (ConfigBotInput, error) {
		return ConfigBotInput{}, fmt.Errorf("%w: %s", ErrInvalidBot, fmt.Sprintf(format, args...))
	}
	name, err := botName(in.Name)
	if err != nil {
		return ConfigBotInput{}, err
	}
	out := ConfigBotInput{Name: name, PrivateChat: in.PrivateChat, Skills: []string{}}
	for _, l := range []struct {
		label string
		ids   []string
		out   *[]string
	}{
		{"管理员", in.Admins, &out.Admins},
		{"私聊白名单", in.PrivateAllowlist, &out.PrivateAllowlist},
		{"启用的群", in.Groups, &out.Groups},
	} {
		*l.out = []string{}
		for _, id := range l.ids {
			switch {
			case id == "":
				return invalid("%s：ID 不能为空", l.label)
			case strings.ContainsFunc(id, func(r rune) bool {
				return unicode.IsSpace(r) || !unicode.IsGraphic(r) || strings.ContainsRune(config.KeySeparator, r)
			}):
				return invalid("%s：ID“%s”不能包含空白、冒号或不可见字符", l.label, id)
			case !slices.Contains(*l.out, id):
				*l.out = append(*l.out, id)
			}
		}
	}
	if modes := config.PrivateChatModes(); !slices.Contains(modes, in.PrivateChat) {
		return invalid("私聊开放范围必须是 %s 之一", strings.Join(modes, "、"))
	}
	for _, t := range []struct {
		label, name string
		out         *string
	}{
		{"私聊默认模板", in.PrivateTemplate, &out.PrivateTemplate},
		{"群聊默认模板", in.GroupTemplate, &out.GroupTemplate},
	} {
		*t.out = cmp.Or(strings.TrimSpace(t.name), defaultPersonaTemplate)
		if _, ok := s.o.personas.Template(*t.out); !ok {
			return invalid("%s“%s”不存在", t.label, *t.out)
		}
	}
	for _, name := range in.Skills {
		if slices.Contains(out.Skills, name) {
			continue
		}
		if problem := s.configSkillProblem(name); problem != "" {
			return invalid("%s", problem)
		}
		out.Skills = append(out.Skills, name)
	}
	return out, nil
}

// skillProblem says in Chinese why a panel bot cannot use the skill name, or
// returns "" when it can: the skill is registered, applies to private chats
// and has all its tools registered.
func (s *PanelBotService) skillProblem(name string) string {
	sk, ok := s.o.skills.Skill(name)
	switch {
	case !ok:
		return fmt.Sprintf("技能“%s”不存在", name)
	case !slices.Contains(sk.Conversations, conloomapi.ConvPrivate):
		return fmt.Sprintf("技能“%s”不能在私聊里用", name)
	case s.o.skills.Check([]string{name}) != nil:
		return fmt.Sprintf("技能“%s”缺少工具，暂时不能用", name)
	}
	return ""
}

// PrivateSkills returns the skills a panel bot may enable, by name: the
// registered skills that apply to private chats and have all their tools
// registered.
func (s *PanelBotService) PrivateSkills() []SkillInfo {
	return s.skillInfos(s.skillProblem)
}

// ConfigSkills returns the skills a config bot may enable, by name: the
// registered skills that have all their tools registered, whatever
// conversations they apply to.
func (s *PanelBotService) ConfigSkills() []SkillInfo {
	return s.skillInfos(s.configSkillProblem)
}

// skillInfos returns the registered skills, by name, for which problem
// returns "".
func (s *PanelBotService) skillInfos(problem func(name string) string) []SkillInfo {
	out := []SkillInfo{}
	for _, sk := range s.o.skills.Skills() {
		if problem(sk.Name) == "" {
			out = append(out, SkillInfo{Name: sk.Name, Description: sk.Description, Conversations: slices.Clone(sk.Conversations)})
		}
	}
	return out
}

// publish replaces the directory's panel bots with bots, in the store's
// order (by creation, then ID), each as it takes effect (see Load); warn logs
// each problem.
func (s *PanelBotService) publish(bots []store.PanelBot, warn bool) {
	bots = slices.SortedStableFunc(slices.Values(bots), func(a, b store.PanelBot) int {
		return cmp.Or(a.CreatedAt.Compare(b.CreatedAt), cmp.Compare(a.ID, b.ID))
	})
	entries := make([]botdir.Entry, 0, len(bots))
	for _, b := range bots {
		e := botdir.Entry{Bot: botdir.PanelBotConfig(b)}
		if len(s.templateFallback(&e)) > 0 && warn {
			s.o.log.Warn("panel bot's persona template no longer exists; using default", "bot", b.ID, "template", b.PersonaTemplate)
		}
		e.Bot.Skills = slices.DeleteFunc(e.Bot.Skills, func(name string) bool {
			problem := s.skillProblem(name)
			if problem == "" {
				return false
			}
			e.Problems = append(e.Problems, problem)
			if warn {
				s.o.log.Warn("panel bot's skill is not usable; leaving it out", "bot", b.ID, "skill", name, "problem", problem)
			}
			return true
		})
		entries = append(entries, e)
	}
	s.o.bots.SetPanel(entries)
}

// publishConfig publishes the config bots to the directory, each as it takes
// effect with its settings among stored (see Load); warn logs each problem.
func (s *PanelBotService) publishConfig(stored []store.BotSettings, warn bool) {
	byID := make(map[string]store.BotSettings, len(stored))
	for _, r := range stored {
		byID[r.BotInstanceID] = r
	}
	configBots := s.o.bots.ConfigBots()
	entries := make([]botdir.Entry, 0, len(configBots))
	for _, b := range configBots {
		if settings, ok := byID[b.ID]; ok {
			b = botdir.ConfigBot(b, settings)
		}
		e := botdir.Entry{Bot: b}
		for _, name := range s.templateFallback(&e) {
			if warn {
				s.o.log.Warn("config bot's persona template does not exist; using default", "bot", b.ID, "template", name)
			}
		}
		e.Bot.Skills = slices.DeleteFunc(slices.Clone(e.Bot.Skills), func(name string) bool {
			problem := s.configSkillProblem(name)
			if problem == "" {
				return false
			}
			e.Problems = append(e.Problems, problem)
			if warn {
				s.o.log.Warn("config bot's skill is not usable; leaving it out", "bot", b.ID, "skill", name, "problem", problem)
			}
			return true
		})
		entries = append(entries, e)
	}
	s.o.bots.SetConfig(entries)
}

// configSkillProblem says in Chinese why a config bot cannot use the skill
// name, or returns "" when it can: the skill is registered and has all its
// tools registered.
func (s *PanelBotService) configSkillProblem(name string) string {
	if _, ok := s.o.skills.Skill(name); !ok {
		return fmt.Sprintf("技能“%s”不存在", name)
	}
	if s.o.skills.Check([]string{name}) != nil {
		return fmt.Sprintf("技能“%s”缺少工具，暂时不能用", name)
	}
	return ""
}

// templateFallback makes e use defaultPersonaTemplate instead of each of its
// private and group persona templates that does not exist, noting the
// problem once per name, and returns the names it replaced.
func (s *PanelBotService) templateFallback(e *botdir.Entry) []string {
	var missing []string
	for _, name := range []*string{&e.Bot.PrivateTemplate, &e.Bot.GroupTemplate} {
		if _, ok := s.o.personas.Template(*name); ok {
			continue
		}
		if !slices.Contains(missing, *name) {
			missing = append(missing, *name)
			e.Problems = append(e.Problems, fmt.Sprintf("人设模板“%s”已不存在，暂用 %s", *name, defaultPersonaTemplate))
		}
		*name = defaultPersonaTemplate
	}
	return missing
}

// audit records a change of the panel bot id by actor as kind. A failed write
// is only logged: the change is already stored.
func (s *PanelBotService) audit(ctx context.Context, id, kind, actor string, detail any) {
	b, err := json.Marshal(detail)
	if err == nil {
		err = s.o.st.InsertAudit(context.WithoutCancel(ctx), store.AuditEvent{
			ID: ids.New("aud"), BotInstanceID: id, Kind: kind, Actor: actor, Target: id,
			Detail: string(b), CreatedAt: s.o.now().UTC(),
		})
	}
	if err != nil {
		s.o.log.Error("panel bot changed but audit failed", "bot", id, "kind", kind, "error", err)
	}
}

// auditDetail is the detail of a panel.bot.create or panel.bot.update audit
// event: the bot's settings as stored.
func auditDetail(in PanelBotInput) any {
	return struct {
		Name            string   `json:"name"`
		PersonaTemplate string   `json:"persona_template"`
		Skills          []string `json:"skills"`
	}{in.Name, in.PersonaTemplate, in.Skills}
}

// configAuditDetail is the detail of a panel.bot.update audit event of a
// config bot whose settings went from before to after: the settings changed,
// by their names in the panel API, and the stored name, private range,
// persona templates and skills. The platform IDs of the admins, the private
// allowlist and the groups are left out.
func configAuditDetail(before, after store.BotSettings) any {
	changed := []string{}
	for _, f := range []struct {
		name string
		same bool
	}{
		{"name", before.Name == after.Name},
		{"admins", slices.Equal(before.Admins, after.Admins)},
		{"private_chat", before.PrivateChat == after.PrivateChat},
		{"private_allowlist", slices.Equal(before.PrivateAllowlist, after.PrivateAllowlist)},
		{"groups", slices.Equal(before.Groups, after.Groups)},
		{"private_template", before.PrivateTemplate == after.PrivateTemplate},
		{"group_template", before.GroupTemplate == after.GroupTemplate},
		{"skills", slices.Equal(before.Skills, after.Skills)},
	} {
		if !f.same {
			changed = append(changed, f.name)
		}
	}
	return struct {
		Changed         []string `json:"changed"`
		Name            string   `json:"name"`
		PrivateChat     string   `json:"private_chat"`
		PrivateTemplate string   `json:"private_template"`
		GroupTemplate   string   `json:"group_template"`
		Skills          []string `json:"skills"`
	}{changed, after.Name, after.PrivateChat, after.PrivateTemplate, after.GroupTemplate, after.Skills}
}
