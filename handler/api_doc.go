package handler

import (
	"github.com/claw-studio/L3_AI_BFF/model"
	"github.com/gin-gonic/gin"
)

type EndpointParams struct {
	Path  map[string]string `json:"path,omitempty"`
	Query map[string]string `json:"query,omitempty"`
	Body  string            `json:"body,omitempty"`
}

type EndpointBackend struct {
	Type  string   `json:"type"`
	Calls []string `json:"calls,omitempty"`
}

type EndpointEntry struct {
	Method   string           `json:"method"`
	Path     string           `json:"path"`
	Summary  string           `json:"summary"`
	Module   string           `json:"module"`
	Params   *EndpointParams  `json:"params,omitempty"`
	Response string           `json:"response_example,omitempty"`
	Backend  *EndpointBackend `json:"backend,omitempty"`
}

var endpointRegistry = []EndpointEntry{
	// ======================== 健康检查 ========================
	{
		Method:  "GET",
		Path:    "/healthz",
		Summary: "健康检查",
		Module:  "系统",
		Response: `{"code":0,"message":"ok","data":{"status":"healthy"}}`,
		Backend: &EndpointBackend{Type: "bff_local"},
	},

	// ======================== 认证 ========================
	{
		Method:  "POST",
		Path:    "/api/auth/login",
		Summary: "用户登录，返回JWT Token",
		Module:  "认证",
		Params: &EndpointParams{
			Body: `{"username":"用户名","password":"密码(>=8位)"}`,
		},
		Response: `{"code":0,"data":{"uid":"user_001","username":"admin","token":"eyJ...","role":"admin"}}`,
		Backend:  &EndpointBackend{Type: "pure_proxy", Calls: []string{"POST a1_vault:/api/auth/login"}},
	},
	{
		Method:  "GET",
		Path:    "/api/auth/me",
		Summary: "获取当前登录用户信息(uid/username/role)",
		Module:  "认证",
		Response: `{"code":0,"data":{"uid":"user_001","username":"admin","role":"admin"}}`,
		Backend:  &EndpointBackend{Type: "bff_local", Calls: []string{"从JWT Token解析，不调下游"}},
	},

	// ======================== AI模型 ========================
	{
		Method:  "GET",
		Path:    "/api/models",
		Summary: "查询可用AI模型列表",
		Module:  "AI模型",
		Params: &EndpointParams{
			Query: map[string]string{"provider": "可选，按提供商过滤"},
		},
		Response: `{"count":6,"models":[{"id":"deepseek/deepseek-chat","name":"DeepSeek V3 Chat","provider":"deepseek","context_limit":65536,"recommended_for":"通用对话","tags":["fast","cheap"]}]}`,
		Backend:  &EndpointBackend{Type: "pure_proxy", Calls: []string{"GET ai_provider:/api/models"}},
	},

	// ======================== 写作风格 ========================
	{
		Method:  "GET",
		Path:    "/api/skill/status",
		Summary: "查询技能注册表服务状态",
		Module:  "写作风格",
		Backend: &EndpointBackend{Type: "pure_proxy", Calls: []string{"GET skill_registry:/api/skill/status"}},
	},
	{
		Method:  "GET",
		Path:    "/api/skill/list",
		Summary: "查询可用写作风格列表",
		Module:  "写作风格",
		Params: &EndpointParams{
			Query: map[string]string{"category": "可选，按分类过滤", "search": "可选，按名称搜索"},
		},
		Response: `{"skills":[{"skill_id":"xhs_grass_v1","version":"1.1.0","name":"小红书种草v1","description":"适合美食/生活种草场景","category":"preset","status":"active"}],"total":5}`,
		Backend:  &EndpointBackend{Type: "pure_proxy", Calls: []string{"GET skill_registry:/api/skill/list"}},
	},
	{
		Method:  "GET",
		Path:    "/api/skill/{skillId}",
		Summary: "查询指定写作风格详情",
		Module:  "写作风格",
		Params: &EndpointParams{
			Path:  map[string]string{"skillId": "风格ID，如 xhs_grass_v1"},
			Query: map[string]string{"version": "可选，指定版本号"},
		},
		Backend: &EndpointBackend{Type: "pure_proxy", Calls: []string{"GET skill_registry:/api/skill/{id}"}},
	},

	// ======================== 任务管理 ========================
	{
		Method:  "POST",
		Path:    "/api/task/create",
		Summary: "创建写作任务，返回task_id（BFF本地生成，不调下游）",
		Module:  "任务管理",
		Params: &EndpointParams{
			Body: `{"topic":"创作主题","platform":"xhs|fanqie|wechat|yuewen","skill_id":"xhs_grass_v1","model":"deepseek-chat","account_ids":["acc_xxx"]}`,
		},
		Response: `{"code":0,"data":{"task_id":"task_abc123def456"}}`,
		Backend:  &EndpointBackend{Type: "bff_local"},
	},
	{
		Method:  "GET",
		Path:    "/api/task/list",
		Summary: "任务列表（分页、搜索）",
		Module:  "任务管理",
		Params: &EndpointParams{
			Query: map[string]string{"page": "页码，默认1", "size": "每页条数，默认20", "q": "可选，按书名模糊搜索"},
		},
		Response: `{"tasks":[{...}],"total":10}`,
		Backend:  &EndpointBackend{Type: "enrichment", Calls: []string{"GET session_mgr:/api/task/list"}},
	},
	{
		Method:  "GET",
		Path:    "/api/task/:tid",
		Summary: "查询单个任务详情",
		Module:  "任务管理",
		Params: &EndpointParams{
			Path: map[string]string{"tid": "任务ID"},
		},
		Backend: &EndpointBackend{Type: "pure_proxy", Calls: []string{"GET session_mgr:/api/task/{id}"}},
	},
	{
		Method:  "POST",
		Path:    "/api/task/:tid/update",
		Summary: "更新任务元数据（书名/卷名/标题/章节号）",
		Module:  "任务管理",
		Params: &EndpointParams{
			Path: map[string]string{"tid": "任务ID"},
			Body: `{"novel_name":"小说名","volume_name":"卷名","title":"章节标题","chapter_number":1,"account_id":"acc_xxx"}`,
		},
		Backend: &EndpointBackend{Type: "pure_proxy", Calls: []string{"POST session_mgr:/api/task/{id}/update"}},
	},
	{
		Method:  "DELETE",
		Path:    "/api/task/:tid",
		Summary: "删除任务及其所有关联数据（会话/草稿/记忆）",
		Module:  "任务管理",
		Params: &EndpointParams{
			Path: map[string]string{"tid": "任务ID"},
		},
		Backend: &EndpointBackend{Type: "pure_proxy", Calls: []string{"DELETE session_mgr:/api/task/{id}"}},
	},
	{
		Method:  "GET",
		Path:    "/api/task/:tid/sessions",
		Summary: "获取任务下所有会话列表",
		Module:  "任务管理",
		Params: &EndpointParams{
			Path: map[string]string{"tid": "任务ID"},
		},
		Response: `{"sessions":[{"session_id":"sess_xxx","status":"archived","draft_version":3,"chapter_number":1,"volume_name":"卷一"}],"count":5}`,
		Backend:  &EndpointBackend{Type: "pure_proxy", Calls: []string{"GET session_mgr:/api/task/{id}/sessions"}},
	},
	{
		Method:  "GET",
		Path:    "/api/task/:tid/timeline",
		Summary: "获取任务时间线（会话创建/归档事件，支持游标分页）",
		Module:  "任务管理",
		Params: &EndpointParams{
			Path:  map[string]string{"tid": "任务ID"},
			Query: map[string]string{"limit": "返回条数，默认50", "cursor": "游标，用于翻页"},
		},
		Backend: &EndpointBackend{Type: "enrichment", Calls: []string{"GET session_mgr:/api/task/{id}/timeline"}},
	},
	{
		Method:  "GET",
		Path:    "/api/task/:tid/messages",
		Summary: "获取任务聊天消息列表",
		Module:  "任务管理",
		Params: &EndpointParams{
			Path: map[string]string{"tid": "任务ID"},
		},
		Backend: &EndpointBackend{Type: "pure_proxy", Calls: []string{"GET session_mgr:/api/task/{id}/messages"}},
	},
	{
		Method:  "POST",
		Path:    "/api/task/:tid/message",
		Summary: "向任务发送消息（支持chat自由对话/edit修改章节两种模式）",
		Module:  "任务管理",
		Params: &EndpointParams{
			Path: map[string]string{"tid": "任务ID"},
			Body: `{"text":"消息内容","mode":"chat|edit","target_session_id":"可选，edit模式指定目标会话","draft_version":1}`,
		},
		Backend: &EndpointBackend{Type: "enrichment", Calls: []string{"POST session_mgr:/api/task/{id}/message"}},
	},
	{
		Method:  "DELETE",
		Path:    "/api/task/:tid/messages",
		Summary: "清空任务聊天消息",
		Module:  "任务管理",
		Params: &EndpointParams{
			Path: map[string]string{"tid": "任务ID"},
		},
		Backend: &EndpointBackend{Type: "pure_proxy", Calls: []string{"DELETE session_mgr:/api/task/{id}/messages"}},
	},
	{
		Method:  "POST",
		Path:    "/api/task/alloc_skill",
		Summary: "根据平台/主题/风格自动匹配最合适的写作技能",
		Module:  "任务管理",
		Params: &EndpointParams{
			Body: `{"platform":"xhs(必填)","theme":"主题(可选)","style":"风格(可选)"}`,
		},
		Backend: &EndpointBackend{Type: "enrichment", Calls: []string{"POST skill_registry:/api/skill/alloc"}},
	},
	{
		Method:  "POST",
		Path:    "/api/task/auto-publish/start",
		Summary: "启动自动发布（后台逐章写稿→发布）",
		Module:  "任务管理",
		Params: &EndpointParams{
			Body: `{"task_id":"task_xxx","platform":"xhs","accounts":["acc_foo"],"skill_id":"xhs_grass_v1","novel_name":"小说名","volume_name":"卷名"}`,
		},
		Backend: &EndpointBackend{
			Type: "aggregation",
			Calls: []string{
				"GET a1_vault:/api/account/list",
				"GET session_mgr:/api/task/{tid}",
				"POST session_mgr:/api/task/{tid}/wake (循环)",
				"GET session_mgr:/api/session/{sid} (轮询)",
				"GET session_mgr:/api/session/{sid}/draft",
				"POST session_mgr:/api/session/{sid}/close",
				"POST workflow:/api/task/{tid}/publish",
				"POST session_mgr:/api/task/{tid}/update",
			},
		},
	},
	{
		Method:  "POST",
		Path:    "/api/task/stop",
		Summary: "停止自动发布",
		Module:  "任务管理",
		Backend: &EndpointBackend{Type: "bff_local"},
	},
	{
		Method:  "POST",
		Path:    "/api/task/finish",
		Summary: "完成/完结自动发布（写最后一章并发布）",
		Module:  "任务管理",
		Params: &EndpointParams{
			Body: `{"task_id":"task_xxx","user_id":"user_001"}`,
		},
		Backend: &EndpointBackend{
			Type: "aggregation",
			Calls: []string{
				"GET session_mgr:/api/task/{tid}",
				"POST session_mgr:/api/task/{tid}/wake (is_finale=true)",
				"同auto-publish/start的后续流程",
			},
		},
	},

	// ======================== 书籍内容 ========================
	{
		Method:  "GET",
		Path:    "/api/task/:tid/book/info",
		Summary: "获取书籍结构信息：所有卷名、每卷下的章节树",
		Module:  "书籍内容",
		Params: &EndpointParams{
			Path: map[string]string{"tid": "任务ID"},
		},
		Response: `{"task_id":"...","novel_name":"小说名","total_volumes":2,"total_chapters":10,"volumes":[{"volume_name":"卷一","chapter_count":5,"chapters":[{"chapter_number":1,"session_id":"sess_xxx","title":"第一章标题","status":"archived","draft_version":3,"created_at":"...","archived_at":"..."}]}]}`,
		Backend: &EndpointBackend{
			Type: "aggregation",
			Calls: []string{
				"GET session_mgr:/api/task/{tid} → 取novel_name",
				"GET session_mgr:/api/task/{tid}/sessions → 取全部session",
				"BFF端按volume_name分组+排序构建卷章树",
			},
		},
	},
	{
		Method:  "GET",
		Path:    "/api/task/:tid/book/content",
		Summary: "获取指定卷、章的正文内容",
		Module:  "书籍内容",
		Params: &EndpointParams{
			Path:  map[string]string{"tid": "任务ID"},
			Query: map[string]string{"volume_name": "卷名(必填)", "chapter_number": "章号，整数(必填)"},
		},
		Response: `{"task_id":"...","volume_name":"卷一","chapter_number":1,"session_id":"sess_xxx","chapter_title":"第一章 初入江湖","content":"(Markdown正文)","draft_version":3,"created_at":"..."}`,
		Backend: &EndpointBackend{
			Type: "aggregation",
			Calls: []string{
				"GET session_mgr:/api/task/{tid}/sessions → 按volume_name+chapter_number定位session_id",
				"GET session_mgr:/api/session/{sid}/draft → 读取chapter_title+content",
			},
		},
	},

	// ======================== 会话管理 ========================
	{
		Method:  "GET",
		Path:    "/api/session/list",
		Summary: "会话列表（支持按task_id过滤）",
		Module:  "会话管理",
		Params: &EndpointParams{
			Query: map[string]string{"task_id": "可选，按任务ID过滤", "page": "页码，默认1", "size": "每页条数，默认20"},
		},
		Backend: &EndpointBackend{Type: "enrichment", Calls: []string{"GET session_mgr:/api/sessions"}},
	},
	{
		Method:  "POST",
		Path:    "/api/session/create",
		Summary: "创建会话，触发AI自动写稿",
		Module:  "会话管理",
		Params: &EndpointParams{
			Body: `{"task_id":"task_xxx(必填)","skill_id":"xhs_grass_v1","model":"deepseek-chat","topic":"创作主题","platform":"xhs","account_id":"acc_xxx","novel_name":"小说名"}`,
		},
		Response: `{"session_id":"sess_xxx","task_id":"task_xxx","status":"CREATED","cwd_path":"/tmp/sm_demo/...","draft_version":0}`,
		Backend:  &EndpointBackend{Type: "enrichment", Calls: []string{"POST session_mgr:/api/session/create"}},
	},
	{
		Method:  "POST",
		Path:    "/api/session/:sid/message",
		Summary: "追加修改意见（多轮对话）",
		Module:  "会话管理",
		Params: &EndpointParams{
			Path: map[string]string{"sid": "会话ID"},
			Body: `{"text":"修改意见(必填，最长4000字)","draft_version":1}`,
		},
		Backend: &EndpointBackend{Type: "enrichment", Calls: []string{"POST session_mgr:/api/session/{id}/send"}},
	},
	{
		Method:  "POST",
		Path:    "/api/session/:sid/close",
		Summary: "关闭/归档会话，自动生成记忆摘要",
		Module:  "会话管理",
		Params: &EndpointParams{
			Path: map[string]string{"sid": "会话ID"},
		},
		Response: `{"session_id":"sess_xxx","status":"archived"}`,
		Backend:  &EndpointBackend{Type: "pure_proxy", Calls: []string{"POST session_mgr:/api/session/{id}/close"}},
	},
	{
		Method:  "GET",
		Path:    "/api/session/:sid/draft",
		Summary: "获取当前稿子内容",
		Module:  "会话管理",
		Params: &EndpointParams{
			Path: map[string]string{"sid": "会话ID"},
		},
		Response: `{"session_id":"sess_xxx","draft":"(Markdown内容)","chapter_title":"章节标题","draft_version":3}`,
		Backend:  &EndpointBackend{Type: "enrichment", Calls: []string{"GET session_mgr:/api/session/{id}/draft"}},
	},

	// ======================== 发布 ========================
	{
		Method:  "POST",
		Path:    "/api/task/:tid/publish",
		Summary: "发布单个章节到目标平台（番茄/小红书/微信/阅文）",
		Module:  "发布",
		Params: &EndpointParams{
			Path: map[string]string{"tid": "任务ID"},
			Body: `{"draft_version":3,"session_id":"sess_xxx(必填)","platform":"xhs(必填)","accounts":["acc_foo"],"skill_id":"xhs_grass_v1","novel_name":"书名","title":"章节标题","volume_name":"卷名","chapter_number":1}`,
		},
		Backend: &EndpointBackend{
			Type: "enrichment",
			Calls: []string{
				"GET a1_vault:/api/account/list → 解析账号完整信息",
				"POST workflow:/api/task/{tid}/publish → 发布到平台",
				"POST session_mgr:/api/task/{tid}/update → 异步更新任务元数据",
			},
		},
	},
	{
		Method:  "GET",
		Path:    "/api/task/:tid/publish/list",
		Summary: "获取任务的发布记录列表",
		Module:  "发布",
		Params: &EndpointParams{
			Path: map[string]string{"tid": "任务ID"},
		},
		Backend: &EndpointBackend{Type: "enrichment", Calls: []string{"POST dashboard:/api/dashboard/query (BFF将GET转为POST)"}},
	},
	{
		Method:  "GET",
		Path:    "/api/publish/get_status",
		Summary: "查询当前发布状态（idle/publishing/failed）及自动发布运行状态",
		Module:  "发布",
		Params: &EndpointParams{
			Query: map[string]string{"task_id": "任务ID(必填)"},
		},
		Response: `{"latest_volume_name":"卷一","latest_chapter_number":5,"active_session_id":"sess_xxx","publish_status":"publishing","is_auto_publish_running":true}`,
		Backend: &EndpointBackend{
			Type: "aggregation",
			Calls: []string{
				"GET session_mgr:/api/task/{tid} → 取volume_name+published_chapter_count",
				"GET workflow:/api/task/{tid}/status → 取workflow发布状态",
				"BFF内存中检查auto_publish_job状态",
			},
		},
	},
	{
		Method:  "GET",
		Path:    "/api/publish/history",
		Summary: "获取已发布章节历史",
		Module:  "发布",
		Params: &EndpointParams{
			Query: map[string]string{"task_id": "任务ID(必填)"},
		},
		Response: `{"task_id":"...","histories":[{"session_id":"sess_xxx","chapter_number":5,"volume_name":"卷一","created_at":"...","finished_at":"..."}]}`,
		Backend: &EndpointBackend{
			Type: "aggregation",
			Calls: []string{
				"GET session_mgr:/api/task/{tid} → 取published_chapter_count",
				"GET session_mgr:/api/task/{tid}/sessions → 取全部session",
				"GET workflow:/api/task/{tid}/status → 补充活跃发布会话",
				"BFF端按chapter_number过滤+排序",
			},
		},
	},
	{
		Method:  "GET",
		Path:    "/api/publish/session",
		Summary: "获取单个发布会话详情（含发布结果：成功/失败/错误码）",
		Module:  "发布",
		Params: &EndpointParams{
			Query: map[string]string{"task_id": "任务ID(必填)", "platform_session_id": "平台会话ID(必填)"},
		},
		Response: `{"task_id":"...","platform_session_id":"sess_xxx","chapter_number":3,"volume_name":"卷一","status":"done","created_at":"...","finished_at":"...","publish_results":[{"accountId":"acc_foo","platform":"xhs","status":"ok","postId":"post_123"}],"source":"workflow"}`,
		Backend: &EndpointBackend{
			Type: "aggregation",
			Calls: []string{
				"GET workflow:/api/task/{tid}/status → 主数据源",
				"GET session_mgr:/api/task/{tid}/sessions → 回退补历史数据",
			},
		},
	},

	// ======================== 看板 ========================
	{
		Method:  "GET",
		Path:    "/api/dashboard/query",
		Summary: "查询看板数据（总发布数/阅读量/点赞/评论/分享）",
		Module:  "看板",
		Params: &EndpointParams{
			Query: map[string]string{
				"platform":     "按平台过滤，如 fanqie",
				"platforms":    "多平台过滤，逗号分隔",
				"accountIds":   "按账号ID过滤",
				"startTime":    "起始时间",
				"endTime":      "结束时间",
				"page":         "页码",
				"size":         "每页条数",
			},
		},
		Response: `{"totalPosts":10,"totalViews":1000,"totalLikes":50,"totalComments":20,"totalShares":5,"items":[...]}`,
		Backend:  &EndpointBackend{Type: "enrichment", Calls: []string{"POST dashboard:/api/dashboard/query (BFF将GET query转为POST JSON)"}},
	},

	// ======================== 账号管理 ========================
	{
		Method:  "GET",
		Path:    "/api/account/list",
		Summary: "查询已绑定的平台账号列表",
		Module:  "账号管理",
		Params: &EndpointParams{
			Query: map[string]string{"platform": "可选，按平台过滤(fanqie|xhs|wechat|yuewen)"},
		},
		Response: `{"accounts":[{"account_id":"acc_xxx","platform":"fanqie","status":"active","created_at":"..."}]}`,
		Backend:  &EndpointBackend{Type: "enrichment", Calls: []string{"GET a1_vault:/api/account/list"}},
	},
	{
		Method:  "POST",
		Path:    "/api/account/bind",
		Summary: "绑定平台账号（上传cookie凭证）",
		Module:  "账号管理",
		Params: &EndpointParams{
			Body: `{"platform":"fanqie(必填)","credentials_plaintext":"csrf_session_id=...;sessionid=...(必填)","account_id":"可选，覆盖已有账号"}`,
		},
		Backend: &EndpointBackend{Type: "enrichment", Calls: []string{"POST a1_vault:/api/account/bind"}},
	},
	{
		Method:  "POST",
		Path:    "/api/account/unbind",
		Summary: "解绑平台账号（软删除，保留审计记录）",
		Module:  "账号管理",
		Params: &EndpointParams{
			Body: `{"account_id":"acc_xxx(必填)"}`,
		},
		Backend: &EndpointBackend{Type: "enrichment", Calls: []string{"POST a1_vault:/api/account/unbind"}},
	},
	{
		Method:  "POST",
		Path:    "/api/account/credentials",
		Summary: "获取解密后凭证（C1 Publisher发布时调用）",
		Module:  "账号管理",
		Params: &EndpointParams{
			Body: `{"account_id":"acc_xxx(必填)","uid":"user_001(必填)","caller":"c1_publisher"}`,
		},
		Backend: &EndpointBackend{Type: "enrichment", Calls: []string{"POST a1_vault:/api/account/credentials"}},
	},
	{
		Method:  "GET",
		Path:    "/api/account/health/{accountId}",
		Summary: "检查账号cookie是否过期/有效",
		Module:  "账号管理",
		Params: &EndpointParams{
			Path: map[string]string{"accountId": "账号ID"},
		},
		Backend: &EndpointBackend{Type: "pure_proxy", Calls: []string{"GET a1_vault:/api/account/health/{account_id}"}},
	},
	{
		Method:  "GET",
		Path:    "/api/account/credential/{accountId}",
		Summary: "获取明文凭证（前端打开番茄页面功能用）",
		Module:  "账号管理",
		Params: &EndpointParams{
			Path: map[string]string{"accountId": "账号ID"},
		},
		Backend: &EndpointBackend{Type: "pure_proxy", Calls: []string{"GET a1_vault:/api/account/credential/{account_id}"}},
	},

	// ======================== 标题建议 ========================
	{
		Method:  "POST",
		Path:    "/api/novel/title-suggest",
		Summary: "AI生成小说标题建议",
		Module:  "标题建议",
		Params: &EndpointParams{
			Body: `{"content":"小说剧情简介或梗概"}`,
		},
		Response: `{"titles":["标题1","标题2","标题3"],"content":"原始AI回复"}`,
		Backend:  &EndpointBackend{Type: "aggregation", Calls: []string{"POST deepseek:/v1/chat/completions (直接调外部AI)"}},
	},

	// ======================== 管理端 ========================
	{
		Method:  "GET",
		Path:    "/api/admin/users",
		Summary: "用户列表（需admin角色）",
		Module:  "管理端",
		Params: &EndpointParams{
			Query: map[string]string{"page": "页码", "size": "每页条数"},
		},
		Backend: &EndpointBackend{Type: "pure_proxy", Calls: []string{"GET a1_vault:/api/admin/users"}},
	},
	{
		Method:  "POST",
		Path:    "/api/admin/users",
		Summary: "创建用户（需admin角色）",
		Module:  "管理端",
		Params: &EndpointParams{
			Body: `{"username":"用户名","password":"密码(>=8位)","role":"admin|user"}`,
		},
		Backend: &EndpointBackend{Type: "pure_proxy", Calls: []string{"POST a1_vault:/api/admin/users"}},
	},
	{
		Method:  "PUT",
		Path:    "/api/admin/users/{uid}",
		Summary: "更新用户密码/角色（需admin角色）",
		Module:  "管理端",
		Params: &EndpointParams{
			Path: map[string]string{"uid": "用户ID"},
			Body: `{"password":"新密码(可选)","role":"新角色(可选)"}`,
		},
		Backend: &EndpointBackend{Type: "pure_proxy", Calls: []string{"PUT a1_vault:/api/admin/users/{uid}"}},
	},
	{
		Method:  "DELETE",
		Path:    "/api/admin/users/{uid}",
		Summary: "删除用户，软删除（需admin角色）",
		Module:  "管理端",
		Params: &EndpointParams{
			Path: map[string]string{"uid": "用户ID"},
		},
		Backend: &EndpointBackend{Type: "pure_proxy", Calls: []string{"DELETE a1_vault:/api/admin/users/{uid}"}},
	},

	// ======================== WebSocket ========================
	{
		Method:  "GET",
		Path:    "/ws/session/:session_id",
		Summary: "订阅会话流式事件（token/draft_updated/done/error）",
		Module:  "WebSocket",
		Params: &EndpointParams{
			Path: map[string]string{"session_id": "会话ID"},
		},
		Backend: &EndpointBackend{Type: "pure_proxy", Calls: []string{"WS session_mgr:/api/session/{id}/stream"}},
	},
	{
		Method:  "GET",
		Path:    "/ws/chat/:task_id",
		Summary: "订阅任务聊天流（多用户聊天消息实时推送）",
		Module:  "WebSocket",
		Params: &EndpointParams{
			Path: map[string]string{"task_id": "任务ID"},
		},
		Backend: &EndpointBackend{Type: "pure_proxy", Calls: []string{"WS session_mgr:/api/task/{id}/stream"}},
	},
	{
		Method:  "GET",
		Path:    "/ws/task/:task_id",
		Summary: "订阅发布进度（init→publishing→published→md_writing→md_written→done）",
		Module:  "WebSocket",
		Params: &EndpointParams{
			Path: map[string]string{"task_id": "任务ID"},
		},
		Backend: &EndpointBackend{Type: "pure_proxy", Calls: []string{"WS workflow:/ws/task/{id}"}},
	},
}

func ListEndpoints() gin.HandlerFunc {
	return func(c *gin.Context) {
		modules := make([]string, 0)
		seen := make(map[string]bool)
		for _, ep := range endpointRegistry {
			if !seen[ep.Module] {
				seen[ep.Module] = true
				modules = append(modules, ep.Module)
			}
		}

		model.Success(c, gin.H{
			"total":     len(endpointRegistry),
			"modules":   modules,
			"endpoints": endpointRegistry,
		})
	}
}
