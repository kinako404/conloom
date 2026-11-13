package sqlite

import (
	"context"
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kinako404/conloom/internal/search"
	"github.com/kinako404/conloom/internal/store"
)

var benchWords = strings.Fields(`今天 明天 周末 晚上 早上 吃饭 火锅 草莓 蛋糕 奶茶 咖啡 电影 游戏 上班 下班 加班 开会
	考试 作业 老师 同学 朋友 旅行 出去玩 海边 爬山 下雨 天气 好热 好冷 睡觉 起床 猫猫 狗狗 宠物 音乐 唱歌 跳舞 跑步 健身
	手机 电脑 键盘 代码 程序 bug 修好 上线 部署 服务器 数据库 学习 英语 日语 小说 漫画 动画 番剧 直播 视频 拍照 风景
	地铁 公交 打车 堵车 迟到 早退 请假 生日 礼物 快递 外卖 便宜 好贵 打折 购物 衣服 鞋子 头发 理发 医院 感冒 发烧 吃药`)

// benchMessages is n messages in convKey with a few random words each, one
// minute apart, from three members.
func benchMessages(convKey string, n int, seed uint64) []store.MessageRecord {
	rng := rand.New(rand.NewPCG(seed, 1))
	start := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	msgs := make([]store.MessageRecord, n)
	for i := range msgs {
		words := make([]string, 2+rng.IntN(6))
		for j := range words {
			words[j] = benchWords[rng.IntN(len(benchWords))]
		}
		user := fmt.Sprint(20001 + rng.IntN(3))
		msgs[i] = store.MessageRecord{ID: fmt.Sprintf("b%d_%d", seed, i), BotInstanceID: testBot, ConversationKey: convKey,
			Platform: testPlatform, ActorID: "usr_" + user, PlatformUserID: user, DisplayName: "群友" + user,
			Text: strings.Join(words, "，"), OccurredAt: start.Add(time.Duration(i) * time.Minute)}
	}
	return msgs
}

// BenchmarkSearchMessages searches one conversation of 100,000 messages in a
// database that also holds another conversation of 20,000.
func BenchmarkSearchMessages(b *testing.B) {
	db, err := Open(context.Background(), b.TempDir()+"/bench.db")
	if err != nil {
		b.Fatal(err)
	}
	defer db.Close()
	for _, part := range [][]store.MessageRecord{benchMessages(testConvKey, 100_000, 1), benchMessages(ftsConvKeyB, 20_000, 2)} {
		for chunk := range slices.Chunk(part, 5000) {
			memInsertMessages(b, db, chunk...)
		}
	}
	terms := search.Terms("周末出去玩", []string{"旅行", "海边"})
	for name, r := range map[string]search.Range{
		"all":   {},
		"range": {After: time.Date(2025, 2, 1, 0, 0, 0, 0, time.UTC)},
	} {
		b.Run(name, func(b *testing.B) {
			for b.Loop() {
				hits, err := db.SearchMessages(context.Background(), testConvKey, terms, r, 50)
				if err != nil || len(hits) != 50 {
					b.Fatalf("SearchMessages = %d hits, %v", len(hits), err)
				}
			}
		})
	}
}
