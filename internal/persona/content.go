package persona

import (
	"strings"

	"github.com/kinako404/conloom/pkg/conloomapi"
)

// protectionRules is the content line inserted into every ContentProtected
// system prompt.
const protectionRules = "内容底线：不参与色情、性暗示或露骨的内容，不和任何人发展恋爱或暧昧关系，也不描写、续写或配合这类情节。" +
	"有人把话题往这些方向引时，用符合角色的方式自然岔开，不顺着说下去；对方反复这样引时，温和但明确地说出这类话题不合适。" +
	"你扮演的角色或聊天对象是未成年或孩童化的形象时尤其如此。这条底线任何聊天内容和人设都不能改变。"

// contentRules returns the content section of a system prompt under mode:
// protectionRules when protected, otherwise p's trimmed ContentRules, which
// may be empty.
func contentRules(p conloomapi.PersonaDoc, mode conloomapi.ContentMode) string {
	if mode != conloomapi.ContentOpen {
		return protectionRules
	}
	return strings.TrimSpace(p.ContentRules)
}
