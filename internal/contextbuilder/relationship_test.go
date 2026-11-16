package contextbuilder

import "testing"

func TestBuild_RelationshipBeforeDirective(t *testing.T) {
	in := baseInput()
	in.Relationship = "【关系提示】你们很亲近。"
	in.Directive = "【提示】这是任务提示。"
	out := Build(in)
	last := out.Messages[len(out.Messages)-1].Content
	want := "【关系提示】你们很亲近。\n\n【当前消息】发送者：小明\n今天天气如何\n\n【提示】这是任务提示。"
	if last != want {
		t.Fatalf("last message = %q, want %q", last, want)
	}

	without := in
	without.Relationship, without.Directive = "", ""
	if got, base := BaseTokens(in), BaseTokens(without); got <= base {
		t.Errorf("BaseTokens with relationship = %d, without = %d; want greater", got, base)
	}
}
