package memory

import "testing"

func TestLooksLikeContactOrID(t *testing.T) {
	for _, s := range []string{
		"我的手机号是 13800001111",
		"手机 138-0000-1111",
		"电话 +86 138 0000 1111",
		"邮箱 xiaoming@example.com",
		"身份证 123456789012345678",
		"证件号 12345678901234567X",
		"QQ 号 20001",
		"我的qq是2000123",
		"扣扣：20001",
		"微信 xiaoming_01",
		"vx：xm2026",
		"WeChat id: xiao_ming",
		"我的号码是 2000123456",
	} {
		if !LooksLikeContactOrID(s) {
			t.Errorf("LooksLikeContactOrID(%q) = false, want true", s)
		}
	}
	for _, s := range []string{
		"我下周三考试",
		"我 2026 年毕业",
		"我家猫 3 岁了",
		"我喜欢玩qq飞车",
		"我的微信头像是猫",
		"比分是 3:2",
		"我在学 Go 1.26",
	} {
		if LooksLikeContactOrID(s) {
			t.Errorf("LooksLikeContactOrID(%q) = true, want false", s)
		}
	}
}

func TestWithoutRemembered(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"喜欢猫。", "喜欢猫。"},
		{"喜欢猫。（他让我记住）我下周三考试", "喜欢猫。"},
		{"喜欢猫。 （他让我记住）我下周三考试（他让我记住）周末要搬家", "喜欢猫。"},
		{"（他让我记住）我下周三考试", ""},
		{"", ""},
	} {
		if got := WithoutRemembered(c.in); got != c.want {
			t.Errorf("WithoutRemembered(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
