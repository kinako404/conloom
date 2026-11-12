package interest

// stopBigrams are common Chinese function-word and filler bigrams that carry
// no topic and are never kept as terms.
var stopBigrams = setOf(
	"我们", "你们", "他们", "她们", "它们", "咱们", "人家",
	"什么", "怎么", "这个", "那个", "一个", "哪个", "这些", "那些", "这里", "那里", "哪里",
	"没有", "可以", "就是", "不是", "知道", "觉得", "现在", "时候", "还是", "一下",
	"真的", "哈哈", "呵呵", "嘿嘿", "嘻嘻", "应该", "自己", "已经", "因为", "所以",
	"但是", "如果", "这样", "那样", "怎样", "有点", "一点", "一样", "为什", "有什",
	"是不", "不会", "不要", "可能", "大家", "最近", "今天", "明天", "昨天", "然后",
	"还有", "也是", "都是", "我的", "你的", "他的", "她的", "这么", "那么", "多少",
	"其实", "而且", "或者", "虽然", "只是", "感觉", "好像", "确实", "反正", "一直",
	"一起", "东西", "事情", "问题", "地方", "刚才", "刚刚", "马上", "一般", "比较",
	"非常", "特别", "肯定", "当然", "好的", "好吧", "谢谢", "厉害", "牛逼", "卧槽",
	"出来", "起来", "过来", "回来", "下来", "上来", "看看", "试试", "说说", "想想",
)

// stopChars are single-character Chinese function words. Any bigram that
// contains one of them is dropped, which removes most bigrams straddling a
// word boundary (e.g. "在玩" in "我在玩原神"). The cost is that a few real
// words containing these characters (e.g. "有趣") are lost too; the model
// remains the final judge of relevance.
var stopChars = runeSetOf(
	"的了是在我你他她它吗呢吧啊哦呀和也都就还又这那有不个很要会能给把被让对们得么",
)

// stopWords are common English function words and chat fillers.
var stopWords = setOf(
	"the", "and", "you", "this", "that", "what", "with", "for", "are", "was",
	"have", "just", "like", "lol", "is", "it", "in", "on", "to", "of",
	"my", "me", "we", "he", "she", "they", "them", "his", "her", "its",
	"our", "your", "be", "been", "do", "does", "did", "not", "no", "yes",
	"so", "but", "or", "if", "at", "by", "from", "as", "an", "can",
	"will", "would", "could", "should", "about", "there", "here", "then", "than", "too",
	"very", "really", "get", "got", "im", "dont", "lmao", "haha", "hahaha", "ok",
	"okay", "yeah", "yep", "hmm", "um", "uh", "oh", "ah", "all", "any",
	"some", "how", "why", "who", "when", "where", "which", "were", "has", "had",
	"am", "us", "up", "out", "into", "also", "now", "one", "more", "much",
	"thanks", "thx", "pls", "please", "hi", "hello", "hey", "xd", "omg", "wtf",
)

func setOf(words ...string) map[string]struct{} {
	m := make(map[string]struct{}, len(words))
	for _, w := range words {
		m[w] = struct{}{}
	}
	return m
}

func runeSetOf(chars string) map[rune]struct{} {
	m := make(map[rune]struct{})
	for _, r := range chars {
		m[r] = struct{}{}
	}
	return m
}
