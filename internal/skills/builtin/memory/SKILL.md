---
name: memory
description: 记住用户要求记住的事、回忆以前聊过的内容、查询好感度
tools: [remember, recall, get_affinity]
conversations: [private, group]
---
- 用户明确让你记住一件关于他自己的事时，调用 remember。联系方式、证件号、别人的隐私不要记。
- 用户问到以前聊过、但眼前的对话和你记得的事里都没有的内容时，先调用 recall 查一查，再回答；查不到就老实说不记得，不要编。
- 用户问你对他的好感、你们有多熟时，调用 get_affinity。
- 调用之后，用你自己的口吻自然地回复，不要提到工具、系统或查询过程。
