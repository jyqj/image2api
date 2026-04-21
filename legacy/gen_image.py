"""
ChatGPT 图片生成 —— 纯协议版（参考 gpt4free）

关键流程：
  1. GET chatgpt.com/  → 拿 oai-did cookie
  2. POST /backend-api/sentinel/chat-requirements
         body:{"p": get_requirements_token(config)}
     → 返回 chat_token + 可选 proofofwork / turnstile 挑战
  3. 若 proofofwork required → 本地 SHA3-512 暴力解
  4. POST /backend-api/f/conversation (SSE)
     header:
        openai-sentinel-chat-requirements-token: chat_token
        openai-sentinel-proof-token: proof_token
  5. 解析 SSE → 拿 file_id → 下载图片

用法: python gen_image.py "提示词"
"""
import sys, json, time, base64, hashlib, random, os, uuid
from datetime import datetime, timezone
from typing import Optional
from curl_cffi.requests import Session

# ── 配置 ──────────────────────────────────────────────────────────────────────
AUTH_TOKEN = "eyJhbGciOiJSUzI1NiIsImtpZCI6IjE5MzQ0ZTY1LWJiYzktNDRkMS1hOWQwLWY5NTdiMDc5YmQwZSIsInR5cCI6IkpXVCJ9.eyJhdWQiOlsiaHR0cHM6Ly9hcGkub3BlbmFpLmNvbS92MSJdLCJjbGllbnRfaWQiOiJhcHBfWDh6WTZ2VzJwUTl0UjNkRTduSzFqTDVnSCIsImV4cCI6MTc3NjU3NzQ2MSwiaHR0cHM6Ly9hcGkub3BlbmFpLmNvbS9hdXRoIjp7ImNoYXRncHRfYWNjb3VudF9pZCI6Ijk4MTQ3NzUzLTU1ODUtNGI5NS04ZjQ3LWI0YjRmMzJkYTdhOCIsImNoYXRncHRfYWNjb3VudF91c2VyX2lkIjoidXNlci15czE3U3pJQ2k5UjB1ODNvRDZmd0VmYlpfXzk4MTQ3NzUzLTU1ODUtNGI5NS04ZjQ3LWI0YjRmMzJkYTdhOCIsImNoYXRncHRfY29tcHV0ZV9yZXNpZGVuY3kiOiJub19jb25zdHJhaW50IiwiY2hhdGdwdF9wbGFuX3R5cGUiOiJmcmVlIiwiY2hhdGdwdF91c2VyX2lkIjoidXNlci15czE3U3pJQ2k5UjB1ODNvRDZmd0VmYloiLCJ1c2VyX2lkIjoidXNlci15czE3U3pJQ2k5UjB1ODNvRDZmd0VmYloifSwiaHR0cHM6Ly9hcGkub3BlbmFpLmNvbS9wcm9maWxlIjp7ImVtYWlsIjoicXE0MzI1MzlAcHJvdG9uLm1lIiwiZW1haWxfdmVyaWZpZWQiOnRydWV9LCJpYXQiOjE3NzU3MTM0NjEsImlzcyI6Imh0dHBzOi8vYXV0aC5vcGVuYWkuY29tIiwianRpIjoiN2UxYWM5MTctODIxZi00ZTRmLWJlMTctNzJjYTEzYTI5Mzk0IiwibmJmIjoxNzc1NzEzNDYxLCJwd2RfYXV0aF90aW1lIjoxNzcwMjg5MDE0NTM1LCJzY3AiOlsib3BlbmlkIiwiZW1haWwiLCJwcm9maWxlIiwib2ZmbGluZV9hY2Nlc3MiLCJtb2RlbC5yZXF1ZXN0IiwibW9kZWwucmVhZCIsIm9yZ2FuaXphdGlvbi5yZWFkIiwib3JnYW5pemF0aW9uLndyaXRlIl0sInNlc3Npb25faWQiOiJhdXRoc2Vzc18zZTdFbHF2MFIyR2tQcVF4QzJ3ZzVieEQiLCJzbCI6dHJ1ZSwic3ViIjoiYXV0aDB8NjRjZGVkOTllY2QxNTQ0OTI5NzE1NDkyIn0.r9iGZ8V27MuG30AB3o8SJFxnC64hSOYMlFAZbgqm1nzcFhox95EdX492XdP0--HuFO2HXJvxGjVUt3MGnRthCX2blOoO0tB5UhroGOxnPMtepSfghR3-cg8pLTEISdInKLMfuR616BVISbrfMIIdef0bi-Vfww_-J6ZhAlnX93xTZRTZBATuYAA3EXwjI0dwPycfuwtSY2db2CwPqYOm73CPiDmmCo1eLaKw9mB_QbRY6NdApuHNosLsYmsq5KxX0QgQC7MxfLnz5tnT_hK8g9T0lfWyARyIsa-MqJs6XlOCmpjIrWM985w2qW0xEMsoT3Nul5CL0Lwxje_86BU1hhDBCb2AKqqTe3rFiMQTDo_G6tEX-d8OeWLsCXYwgjaSlVv5QsYZzi2M4R2C_HLi8YL7744QxfAmwie3HVpFZFI4sQX9LcmyD_-S6-zBQxJwOO4haQGk_wTRHtsM_x5lK_7iTEjjufsEurnIB5P7jVtxAZS1rBnii5FOxi3ut20Ze0S9SWv7mNF5oX60VlPTMY4kHNOCf4gQ6eD4cFO-n1msb31p3dvqZTQ5NVWKGULgWC2E_REVWLkYtIzUc3qQbzDaJNDzNjpZiQYiiVHxVFftILrxyohS2QxFAs6S0oQ3rTvwtFQakn4SSh0g492394jmCq8YwkgmYdVRqGXTbq4"

PROXY      = None  # 为 None 则走本机默认路由；需要代理时填 "http://user:pass@host:port"
                   # 或把 PROXY_TEMPLATE 填上 → 运行时自动抽活出口
# 本机已开 Mihomo/Clash Verge TUN 模式，直连即可由 TUN 透明转发出国。
# 如果 arxlabs 等外部代理恢复可用想再启用，把下面这行取消注释即可：
# PROXY_TEMPLATE = "http://p9mx1124350-region-Rand-sid-{sid}-t-1:iy2lmzpy@us.arxlabs.io:3010"
PROXY_TEMPLATE = None
BASE_URL   = "https://chatgpt.com"
OUTPUT_DIR = r"C:\Users\Administrator\Documents\gpt2api_images"

# 固定复用的会话 ID：如果有值就在该会话里追加消息；
# 设为 None 则每次都走"新建会话"流程（init → prepare → send），每次重新洗灰度桶。
# 上一个会话 69e1c678 被我们密集轮询打到限流坏了，换成用户抓包里成功过灰度的新会话。
FIXED_CONVERSATION_ID = "69e2205a-b5e4-83e8-8e6a-74d8b0c1941c"

# 灰度未命中时的最大重试次数。每次重试会开新 session（新 oai-did / chat_token）。
MAX_ATTEMPTS = 8

# 用户指定必加的一句（只追加这一句，不要加其他多余的英文约束）。
# 只在提示词包含要渲染的文字（对话、标语、字幕等）时才追加，纯画面不加。
CLARITY_SUFFIX = "\n\nclean readable Chinese text, prioritize text clarity over image details"

# 判断提示词是否需要渲染文字的启发式：包含常见"要出字"的关键词、引号、或
# 冒号后面紧跟中文等。需要时才追加 CLARITY_SUFFIX。
_TEXT_HINT_KWS = (
    "文字", "对话", "台词", "旁白", "标语", "字幕", "标题", "文案",
    "招牌", "横幅", "海报文字", "弹幕", "气泡", "字体",
    "text:", "caption", "subtitle", "title:", "label", "banner", "poster text",
)
_TEXT_HINT_CHARS = ('"', "'", "“", "”", "‘", "’", "「", "」", "『", "』", "：", ":")

def needs_text_rendering(prompt: str) -> bool:
    p = prompt.lower()
    if any(kw.lower() in p for kw in _TEXT_HINT_KWS):
        return True
    # 中文/英文引号里有内容（长度 >= 2 的）才算需要文字
    import re
    if re.search(r'["“‘「『][^"”’」』]{2,}["”’」』]', prompt):
        return True
    return False

UA = ("Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 "
      "(KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36")

# ── 日志 ──────────────────────────────────────────────────────────────────────
def log(msg, level="INFO"):
    ts = datetime.now().strftime("%H:%M:%S")
    icon = "✓" if level=="OK" else "✗" if level=="ERR" else "·"
    print(f"[{ts}] {icon} {msg}", flush=True)

# ── POW 算法（迁移自 gpt4free/openai/proofofwork.py + new.py） ─────────────────
_CORES   = [16, 24, 32]
_SCREENS = [3000, 4000, 6000]
_MAX_ATTEMPTS = 500000

_NAV_KEYS = [
    "webdriver−false", "vendor−Google Inc.", "cookieEnabled−true",
    "pdfViewerEnabled−true", "hardwareConcurrency−32",
    "language−zh-CN", "mimeTypes−[object MimeTypeArray]",
    "userAgentData−[object NavigatorUAData]",
]
_WIN_KEYS = ["innerWidth", "innerHeight", "devicePixelRatio", "screen",
             "chrome", "location", "history", "navigator"]

def _parse_time() -> str:
    now = datetime.now(timezone.utc)
    return now.strftime("%a, %d %b %Y %H:%M:%S GMT")

def _pow_config(user_agent: str) -> list:
    import time as _t
    return [
        random.choice(_CORES) + random.choice(_SCREENS),
        datetime.now(timezone.utc).strftime("%a %b %d %Y %H:%M:%S") + " GMT+0000 (UTC)",
        None,
        random.random(),
        user_agent,
        None,
        "dpl=1440a687921de39ff5ee56b92807faaadce73f13",
        "en-US",
        "en-US,zh-CN",
        0,
        random.choice(_NAV_KEYS),
        "location",
        random.choice(_WIN_KEYS),
        _t.perf_counter(),
        str(uuid.uuid4()),
        "",
        8,
        int(_t.time()),
    ]

def _generate_answer(seed: str, difficulty: str, config: list):
    diff_len = len(difficulty)
    seed_enc = seed.encode()
    p1 = (json.dumps(config[:3],  separators=(',',':'), ensure_ascii=False)[:-1] + ',').encode()
    p2 = (',' + json.dumps(config[4:9], separators=(',',':'), ensure_ascii=False)[1:-1] + ',').encode()
    p3 = (',' + json.dumps(config[10:], separators=(',',':'), ensure_ascii=False)[1:]).encode()
    target = bytes.fromhex(difficulty)
    for i in range(_MAX_ATTEMPTS):
        d1 = str(i).encode()
        d2 = str(i >> 1).encode()
        b64 = base64.b64encode(p1 + d1 + p2 + d2 + p3)
        h = hashlib.sha3_512(seed_enc + b64).digest()
        if h[:diff_len] <= target:
            return b64.decode(), True
    fb = 'wQ8Lk5FbGpA2NcR9dShT6gYjU7VxZ4D' + base64.b64encode(f'"{seed}"'.encode()).decode()
    return fb, False

def get_requirements_token(config: list) -> str:
    seed = format(random.random())
    ans, ok = _generate_answer(seed, "0fffff", config)
    return "gAAAAAC" + ans

def generate_proof_token(required: bool, seed: str, difficulty: str,
                         user_agent: str, proof_config: list) -> Optional[str]:
    if not required:
        return None
    # gpt4free 另一种老 config（更轻量），备用
    if proof_config is None:
        scr = random.choice([3008, 4010, 6000]) * random.choice([1, 2, 4])
        proof_config = [
            scr, _parse_time(), None, 0, user_agent,
            "https://tcr9i.chat.openai.com/v2/35536E1E-65B4-4D96-9D97-6ADB7EFF8147/api.js",
            "dpl=1440a687921de39ff5ee56b92807faaadce73f13", "en", "en-US",
            None,
            "plugins−[object PluginArray]",
            random.choice(["_reactListeningcfilawjnerp", "_reactListening9ne2dfo1i47"]),
            random.choice(["alert", "ontransitionend", "onprogress"]),
        ]
    diff_len = len(difficulty)
    for i in range(100000):
        proof_config[3] = i
        j = json.dumps(proof_config)
        b64 = base64.b64encode(j.encode()).decode()
        h = hashlib.sha3_512((seed + b64).encode()).digest()
        if h.hex()[:diff_len] <= difficulty:
            return "gAAAAAB" + b64
    fb = base64.b64encode(f'"{seed}"'.encode()).decode()
    return "gAAAAABwQ8Lk5FbGpA2NcR9dShT6gYjU7VxZ4D" + fb

# ── Session 工厂 ─────────────────────────────────────────────────────────────
def _probe_proxy(max_tries: int = 20) -> Optional[str]:
    """随机抽 sid，找一个能到达 chatgpt.com 的出口。返回完整 proxy url。"""
    import random, string
    log("实时抽代理出口（chatgpt.com 可达）...")
    for i in range(max_tries):
        sid = "".join(random.choices(string.ascii_letters + string.digits, k=8))
        px  = PROXY_TEMPLATE.format(sid=sid)
        try:
            s = Session(impersonate="chrome131", verify=False, proxy=px, timeout=12)
            r = s.get(BASE_URL + "/", timeout=15)
            if r.status_code == 200 and "oai-did" in str(r.cookies):
                try:
                    info = Session(impersonate="chrome131", verify=False, proxy=px).get(
                        "https://ipinfo.io/json", timeout=10).json()
                    geo = f"{info.get('country','?')} {info.get('org','')[:30]}"
                except Exception:
                    geo = "?"
                log(f"  sid={sid} → 200 ({geo})", "OK")
                return px
        except Exception:
            pass
        log(f"  sid={sid} 不可用")
    log("所有 sid 都不可用", "ERR")
    return None

def new_session(proxy_override: Optional[str] = None) -> Session:
    kw = {"impersonate": "chrome131", "verify": False}
    chosen = proxy_override or PROXY
    if chosen:
        kw["proxy"] = chosen
    s = Session(**kw)
    s.headers.update({
        "user-agent":      UA,
        "accept-language": "en-US,en;q=0.9",
        "origin":          BASE_URL,
        "referer":         BASE_URL + "/",
        "accept":          "*/*",
        "sec-ch-ua":       '"Google Chrome";v="131", "Chromium";v="131", "Not_A Brand";v="24"',
        "sec-ch-ua-mobile":   "?0",
        "sec-ch-ua-platform": '"Windows"',
        "sec-fetch-dest":  "empty",
        "sec-fetch-mode":  "cors",
        "sec-fetch-site":  "same-origin",
    })
    return s

def _http_retry(fn, retries=5, delay=2.0, label="", retry_on_status=()):
    """不稳定代理下的简单重试包装。
    retry_on_status：对指定的 HTTP 状态码（如 429/502/503/504）也触发重试。
    """
    last_exc = None
    last_resp = None
    for i in range(retries):
        try:
            r = fn()
        except Exception as e:
            last_exc = e
            log(f"  {label} 第{i+1}次失败: {type(e).__name__}: {str(e)[:80]}")
            time.sleep(delay)
            continue
        # 如果指定状态码要重试（典型 429）
        if retry_on_status and getattr(r, "status_code", 0) in retry_on_status:
            last_resp = r
            wait = delay * (2 ** i)  # 退避
            log(f"  {label} 第{i+1}次 → {r.status_code}，等 {wait:.0f}s 后重试")
            time.sleep(wait)
            continue
        return r
    if last_resp is not None:
        return last_resp
    raise last_exc

# ── Step 1: oai-did ──────────────────────────────────────────────────────────
def bootstrap(s: Session) -> str:
    log("访问首页拿 oai-did cookie...")
    r = _http_retry(lambda: s.get(BASE_URL + "/", timeout=30), retries=5, label="GET /")
    log(f"  GET / → {r.status_code}", "OK" if r.ok else "ERR")
    did = r.cookies.get("oai-did")
    if not did:
        # 从 cookie jar 里找
        for c in s.cookies.jar if hasattr(s.cookies, "jar") else []:
            name = getattr(c, "name", getattr(c, "key", ""))
            if name == "oai-did":
                did = c.value
                break
    if not did:
        did = str(uuid.uuid4())
        log(f"  未拿到 oai-did，随机生成: {did}")
    else:
        log(f"  oai-did = {did}", "OK")
    return did

# ── Step 1.5: 取已有会话的 current_node（复用固定 conv_id 时用） ───────────────
def get_conversation_head(s: Session, did: str, conv_id: str) -> Optional[str]:
    """
    返回会话最新叶子消息 id（current_node），作为下一条消息的 parent_message_id。
    失败时返回 None，调用方可自行生成随机 parent。
    """
    log(f"拉取会话 {conv_id[:8]}... 当前 head...")
    try:
        r = _http_retry(lambda: s.get(
            BASE_URL + f"/backend-api/conversation/{conv_id}",
            headers={
                "Authorization": f"Bearer {AUTH_TOKEN}",
                "oai-device-id": did,
                "accept":        "*/*",
                "accept-language":"zh-CN,zh;q=0.9,en;q=0.8",
                "oai-language":  "zh-CN",
                "origin":        BASE_URL,
                "referer":       BASE_URL + f"/c/{conv_id}",
            },
            timeout=30,
        ), retries=5, delay=4.0, label="GET conversation",
           retry_on_status=(429, 502, 503, 504))
    except Exception as e:
        log(f"  拉取会话失败: {e}", "ERR")
        return None
    if not r.ok:
        log(f"  → {r.status_code}: {r.text[:200]}", "ERR")
        return None
    try:
        js = r.json()
    except Exception:
        log("  会话响应不是 JSON", "ERR")
        return None
    head = js.get("current_node")
    mapping = js.get("mapping") or {}
    log(f"  current_node = {head}  (mapping 消息数 {len(mapping)})", "OK" if head else "ERR")
    return head

# ── Step 2: /backend-api/sentinel/chat-requirements ──────────────────────────
def get_chat_requirements(s: Session, did: str) -> tuple[str, Optional[dict]]:
    log("请求 chat-requirements...")
    cfg = _pow_config(UA)
    req_token = get_requirements_token(cfg)
    r = _http_retry(lambda: s.post(
        BASE_URL + "/backend-api/sentinel/chat-requirements",
        headers={
            "Authorization": f"Bearer {AUTH_TOKEN}",
            "oai-device-id": did,
            "content-type":  "application/json",
        },
        json={"p": req_token},
        timeout=30,
    ), retries=5, label="chat-requirements")
    log(f"  → {r.status_code}", "OK" if r.ok else "ERR")
    if not r.ok:
        log(f"  body: {r.text[:400]}", "ERR")
        r.raise_for_status()
    data = r.json()
    chat_token = data["token"]
    pow_info   = data.get("proofofwork") or {}
    log(f"  chat_token len={len(chat_token)}  pow_required={pow_info.get('required', False)}", "OK")
    return chat_token, pow_info

# ── Step 2.4: /backend-api/conversation/init（新会话必做）──────────────────────
def init_new_conversation(s: Session, did: str) -> bool:
    """
    新会话场景下，/f/conversation 之前必须先调用 /conversation/init，
    让服务端为即将创建的会话注册一下路由/限流上下文。
    请求体对齐 HAR（_har_body_3_init.json）。
    """
    log("/conversation/init（新会话注册）...")
    headers = {
        "Authorization":  f"Bearer {AUTH_TOKEN}",
        "accept":         "*/*",
        "accept-language":"zh-CN,zh;q=0.9,en;q=0.8,en-GB;q=0.7,en-US;q=0.6",
        "cache-control":  "no-cache",
        "pragma":         "no-cache",
        "priority":       "u=1, i",
        "content-type":   "application/json",
        "oai-device-id":  did,
        "oai-language":   "zh-CN",
        "oai-client-build-number": "5955942",
        "oai-client-version":      "prod-be885abbfcfe7b1f511e88b3003d9ee44757fbad",
        "origin":         BASE_URL,
        "referer":        BASE_URL + "/",
    }
    payload = {
        "gizmo_id":               None,
        "requested_default_model":None,
        "conversation_id":        None,
        "timezone_offset_min":    -480,
        "system_hints":           ["picture_v2"],
    }
    try:
        r = _http_retry(lambda: s.post(
            BASE_URL + "/backend-api/conversation/init",
            headers=headers, json=payload, timeout=30,
        ), retries=4, delay=3.0, label="conversation/init",
           retry_on_status=(429, 502, 503, 504))
    except Exception as e:
        log(f"  init 失败: {e}", "ERR")
        return False
    if not r.ok:
        log(f"  init → {r.status_code}: {r.text[:200]}", "ERR")
        return False
    log(f"  init → 200 OK", "OK")
    return True

# ── Step 2.5: /backend-api/f/conversation/prepare（灰度分桶关键） ─────────────
def prepare_fconversation(s: Session, did: str, chat_token: str, proof_token: Optional[str],
                          conv_id: str, parent_id: str, msg_id: str, prompt: str) -> Optional[str]:
    """
    浏览器真实流程里 conversation 前必须先 prepare。
    返回的 conduit_token 决定本次会话被路由到哪个集群（可能和新 IMG2 灰度相关）。
    """
    log("/f/conversation/prepare 预热（拿 conduit_token）...")
    headers = {
        "Authorization":  f"Bearer {AUTH_TOKEN}",
        "accept":         "*/*",
        "accept-language":"zh-CN,zh;q=0.9,en;q=0.8,en-GB;q=0.7,en-US;q=0.6",
        "cache-control":  "no-cache",
        "pragma":         "no-cache",
        "priority":       "u=1, i",
        "content-type":   "application/json",
        "oai-device-id":  did,
        "oai-language":   "zh-CN",
        "oai-client-build-number": "5955942",
        "oai-client-version":      "prod-be885abbfcfe7b1f511e88b3003d9ee44757fbad",
        "origin":         BASE_URL,
        "referer":        BASE_URL + "/",
        "openai-sentinel-chat-requirements-token": chat_token,
    }
    if proof_token:
        headers["openai-sentinel-proof-token"] = proof_token

    payload = {
        "action":              "next",
        "fork_from_shared_post": False,
        "conversation_id":     conv_id,
        "parent_message_id":   parent_id,
        "model":               "gpt-5-3",
        "client_prepare_state":"none",
        "timezone_offset_min": -480,
        "timezone":            "Asia/Shanghai",
        "conversation_mode":   {"kind": "primary_assistant"},
        "system_hints":        ["picture_v2"],
        "partial_query": {
            "id":      msg_id,
            "author":  {"role": "user"},
            "content": {"content_type": "text", "parts": [prompt]},
        },
        "supports_buffering":  True,
        "supported_encodings": ["v1"],
        "client_contextual_info": {"app_name": "chatgpt.com"},
    }
    try:
        r = _http_retry(lambda: s.post(
            BASE_URL + "/backend-api/f/conversation/prepare",
            headers=headers, json=payload, timeout=30,
        ), retries=4, label="f/conversation/prepare")
    except Exception as e:
        log(f"  prepare 失败（可降级直跑）: {e}", "ERR")
        return None
    if not r.ok:
        log(f"  prepare → {r.status_code}: {r.text[:300]}", "ERR")
        return None
    js = r.json() or {}
    ct = js.get("conduit_token")
    if ct:
        log(f"  conduit_token 已获取 len={len(ct)}", "OK")
    else:
        log(f"  prepare 响应无 conduit_token: {str(js)[:200]}", "ERR")
    return ct

# ── Step 3: f/conversation SSE ───────────────────────────────────────────────
def send_conversation(s: Session, did: str, chat_token: str, proof_token: Optional[str],
                      conv_id: str, parent_id: str, msg_id: str, prompt: str,
                      conduit_token: Optional[str] = None):
    log("发送生图对话请求（SSE）...")
    headers = {
        "Authorization":  f"Bearer {AUTH_TOKEN}",
        "accept":         "text/event-stream",
        "accept-language":"zh-CN,zh;q=0.9,en;q=0.8,en-GB;q=0.7,en-US;q=0.6",
        "cache-control":  "no-cache",
        "pragma":         "no-cache",
        "priority":       "u=1, i",
        "content-type":   "application/json",
        "oai-device-id":  did,
        "oai-language":   "zh-CN",
        "oai-client-build-number": "5955942",
        "oai-client-version":      "prod-be885abbfcfe7b1f511e88b3003d9ee44757fbad",
        "origin":         BASE_URL,
        "referer":        BASE_URL + "/",
        "openai-sentinel-chat-requirements-token": chat_token,
    }
    if proof_token:
        headers["openai-sentinel-proof-token"] = proof_token
    if conduit_token:
        headers["x-conduit-token"] = conduit_token

    payload = {
        "action": "next",
        "messages": [{
            "id":          msg_id,
            "author":      {"role": "user"},
            "create_time": time.time(),
            "content":     {"content_type": "text", "parts": [prompt]},
            "metadata": {
                "developer_mode_connector_ids": [],
                "selected_github_repos":        [],
                "selected_all_github_repos":    False,
                "system_hints":                 ["picture_v2"],
                "serialization_metadata":       {"custom_symbol_offsets": []},
            },
        }],
        "conversation_id":          conv_id,
        "parent_message_id":        parent_id,
        "model":                    "gpt-5-3",
        "client_prepare_state":     "sent",
        "timezone_offset_min":      -480,
        "timezone":                 "Asia/Shanghai",
        "conversation_mode":        {"kind": "primary_assistant"},
        "enable_message_followups": True,
        "system_hints":             ["picture_v2"],
        "supports_buffering":       True,
        "supported_encodings":      ["v1"],
        "client_contextual_info": {
            "is_dark_mode":       False,
            "time_since_loaded":  random.randint(500, 3000),
            "page_height":        1072,
            "page_width":         1724,
            "pixel_ratio":        1.2,
            "screen_height":      1440,
            "screen_width":       2560,
            "app_name":           "chatgpt.com",
        },
        "paragen_cot_summary_display_override": "allow",
        "force_parallel_switch": "auto",
    }

    r = _http_retry(lambda: s.post(
        BASE_URL + "/backend-api/f/conversation",
        headers=headers, json=payload,
        stream=True, timeout=180,
    ), retries=5, label="f/conversation")
    if not r.ok:
        body = r.text
        log(f"  SSE 失败 {r.status_code}: {body[:400]}", "ERR")
        r.raise_for_status()
    log("  SSE 连接成功", "OK")
    return r

# ── Step 4: 解析 SSE，收集 file-service 引用 ─────────────────────────────────
def parse_sse(resp) -> dict:
    new_conv_id   = None
    file_ids      = []
    tool_text     = ""
    collecting    = False
    finish_reason = None

    import re as _re
    file_pat = _re.compile(r"file-service://([A-Za-z0-9_-]+)")
    sediment_pat = _re.compile(r"sediment://([A-Za-z0-9_-]+)")

    def handle(data: str):
        nonlocal new_conv_id, tool_text, collecting, finish_reason
        for m in file_pat.finditer(data):
            fid = m.group(1)
            if fid not in file_ids:
                file_ids.append(fid)
                log(f"  发现图片: file-service://{fid[:16]}...", "OK")
        for m in sediment_pat.finditer(data):
            fid = m.group(1)
            if ("sed:" + fid) not in file_ids:
                file_ids.append("sed:" + fid)
                log(f"  发现图片: sediment://{fid[:16]}...", "OK")

        try:
            obj = json.loads(data)
        except Exception:
            return
        if not isinstance(obj, dict):
            return

        if isinstance(obj.get("v"), dict):
            cid = obj["v"].get("conversation_id")
            if cid and not new_conv_id:
                new_conv_id = cid
                log(f"  conversation_id={cid}", "OK")
            msg = obj["v"].get("message", {})
            if isinstance(msg, dict):
                meta = msg.get("metadata", {}) or {}
                if meta.get("image_gen_task_id"):
                    log(f"  image_gen_task_id={meta['image_gen_task_id']}", "OK")
                if meta.get("finish_details"):
                    finish_reason = meta["finish_details"].get("type")
                c = msg.get("content", {}) or {}
                if c.get("content_type") == "code":
                    collecting = True
                    tool_text = c.get("text", "") or ""
        elif isinstance(obj.get("v"), list) and collecting:
            for p in obj["v"]:
                if isinstance(p, dict) and p.get("p", "").endswith("/content/text") and p.get("o") == "append":
                    tool_text += p.get("v", "")

    try:
        for raw in resp.iter_lines():
            if not raw:
                continue
            if isinstance(raw, bytes):
                raw = raw.decode("utf-8", errors="replace")
            raw = raw.strip()
            if not raw.startswith("data:"):
                continue
            data = raw[5:].strip()
            if data in ("[DONE]", ""):
                break
            handle(data)
    except Exception as e:
        log(f"  SSE 中断（可能代理超时）: {type(e).__name__}: {str(e)[:100]}", "ERR")

    if tool_text.strip():
        log(f"  工具调用参数: {tool_text.strip()[:180]}", "OK")
    if finish_reason:
        log(f"  finish_reason={finish_reason}")
    return {"conversation_id": new_conv_id, "file_ids": file_ids}

# ── Step 4.5: 轮询 conversation 详情抓最终图片 ───────────────────────────────
def _extract_img2_tool_ids(mapping: dict) -> set:
    """返回会话里所有 IMG2 tool 消息的 id 集合（不含 asset_pointer，仅用于 diff）"""
    ids = set()
    for mid, node in mapping.items():
        msg  = (node or {}).get("message") or {}
        auth = msg.get("author") or {}
        meta = msg.get("metadata") or {}
        cont = msg.get("content") or {}
        if (auth.get("role") == "tool"
            and meta.get("async_task_type") == "image_gen"
            and cont.get("content_type") == "multimodal_text"):
            ids.add(mid)
    return ids

def fetch_tool_baseline(s: Session, did: str, conv_id: str) -> set:
    """
    发送前拉一次 conversation，记录已有 IMG2 tool 消息 id 集合，作为 baseline。
    poll 阶段用 current - baseline 识别本次新增的 tool 消息。
    """
    log(f"拉取 baseline（会话 {conv_id[:8]}... 已有 tool 消息）...")
    try:
        r = _http_retry(lambda: s.get(
            BASE_URL + f"/backend-api/conversation/{conv_id}",
            headers={
                "Authorization": f"Bearer {AUTH_TOKEN}",
                "oai-device-id": did,
                "accept":        "*/*",
                "accept-language":"zh-CN,zh;q=0.9,en;q=0.8",
                "oai-language":  "zh-CN",
                "origin":        BASE_URL,
                "referer":       BASE_URL + f"/c/{conv_id}",
            },
            timeout=30,
        ), retries=5, delay=4.0, label="GET conv(baseline)",
           retry_on_status=(429, 502, 503, 504))
    except Exception as e:
        log(f"  baseline 拉取失败: {e}", "ERR")
        return set()
    if not r.ok:
        log(f"  baseline → {r.status_code}", "ERR")
        return set()
    try:
        js = r.json()
    except Exception:
        return set()
    ids = _extract_img2_tool_ids(js.get("mapping") or {})
    log(f"  baseline: {len(ids)} 条历史 IMG2 tool 消息", "OK")
    return ids

def poll_conversation_for_images(s: Session, did: str, conv_id: str,
                                 baseline_tool_ids: Optional[set] = None,
                                 max_wait: int = 900, interval: float = 6.0,
                                 stable_rounds: int = 4,
                                 preview_wait_secs: float = 30.0):
    """
    轮询 conversation，等本次回合的图片稳定。返回 (status, ids)。
    - status:
        "img2"         → 命中灰度桶，返回 IMG2 最新那条 tool 消息的 ids
        "preview_only" → 只出现 1 条 tool 消息，且自第一条 tool 消息出现起
                         经过 preview_wait_secs 秒仍无第 2 条 → 判非灰度，ids 为空
        "timeout"      → 超时，返回兜底（通常为空）
    - 识别规则（实验确认的金标准）：
        灰度桶会产出 **≥ 2 条 tool 消息**（先 preview，后 IMG2 最终）；
        非灰度只产出 1 条。
        实际观察：若进了灰度，第 2 条通常在第 1 条出现后 5–30 秒内就会冒出；
        所以 30 秒仍是 1 条，基本可以判定非灰度，立即重试比死等划算。
    """
    log(f"轮询 conversation（max_wait={max_wait}s, interval={interval}s, "
        f"stable_rounds={stable_rounds}, preview_wait={preview_wait_secs}s, "
        f"baseline={len(baseline_tool_ids or [])}）...")
    import re as _re
    fpat = _re.compile(r"file-service://([A-Za-z0-9_-]+)")
    spat = _re.compile(r"sediment://([A-Za-z0-9_-]+)")
    baseline = baseline_tool_ids or set()

    t0 = time.time()
    last_sed_sig = None
    stable_count = 0
    last_sed = []
    seen_any_tool = False  # 本次回合是否已观察到至少一条新的 IMG2 tool 消息
    first_tool_ts: Optional[float] = None  # 第一条新 tool 消息首次出现的时间
    consecutive_429 = 0

    while time.time() - t0 < max_wait:
        try:
            r = _http_retry(lambda: s.get(
                f"{BASE_URL}/backend-api/conversation/{conv_id}",
                headers={"Authorization": f"Bearer {AUTH_TOKEN}", "oai-device-id": did},
                timeout=30,
            ), retries=3, delay=5.0, label="GET conv",
               retry_on_status=(429, 502, 503, 504))
        except Exception as e:
            log(f"  轮询失败: {e}", "ERR")
            time.sleep(interval)
            continue

        if r.status_code == 429:
            consecutive_429 += 1
            log(f"  轮询被 429 限流（连续 {consecutive_429} 次）", "ERR")
            if consecutive_429 >= 3:
                log("  连续 3 次 429 → 本次 attempt 中止，交外层退避重试", "ERR")
                return ("error", [])
            time.sleep(10)
            continue
        consecutive_429 = 0
        if r.status_code != 200:
            time.sleep(interval)
            continue

        try:
            js = r.json()
        except Exception:
            time.sleep(interval)
            continue

        # 第一次收到完整 mapping 就 dump 一份供调试
        try:
            dbg = os.path.join(OUTPUT_DIR, f"_conv_{conv_id[:8]}.json")
            if not os.path.exists(dbg):
                os.makedirs(OUTPUT_DIR, exist_ok=True)
                with open(dbg, "w", encoding="utf-8") as f:
                    json.dump(js, f, ensure_ascii=False, indent=2)
                log(f"  [debug] conversation 已 dump: {dbg}")
        except Exception:
            pass

        mapping = js.get("mapping") or {}
        # baseline diff：只看本次回合新出现的 IMG2 tool 消息。
        # 如果 baseline 为空，就看全部（新建会话场景）。
        all_tool_ids = _extract_img2_tool_ids(mapping)
        new_tool_ids = all_tool_ids - baseline if baseline else all_tool_ids

        # 采集每条 tool 消息的详细元数据，用于区分"快速预览 vs IMG2 最终"
        # tool_records: [{mid, create_time, model_slug, recipient, is_img2_signature, file_ids, sed_ids}]
        tool_records = []
        final_ids, sed_ids = [], []
        for mid in new_tool_ids:
            m = mapping.get(mid) or {}
            msg     = m.get("message") or {}
            author  = msg.get("author") or {}
            content = msg.get("content") or {}
            meta    = msg.get("metadata") or {}

            is_img2 = (
                author.get("role") == "tool"
                and meta.get("async_task_type") == "image_gen"
                and content.get("content_type") == "multimodal_text"
            )
            if not is_img2:
                continue
            seen_any_tool = True

            rec_fids, rec_sids = [], []
            for p in (content.get("parts") or []):
                if isinstance(p, dict):
                    aid = p.get("asset_pointer") or ""
                    for hit in fpat.finditer(aid):
                        fid = hit.group(1)
                        if fid not in rec_fids: rec_fids.append(fid)
                        if fid not in final_ids: final_ids.append(fid)
                    for hit in spat.finditer(aid):
                        fid = hit.group(1)
                        if fid not in rec_sids: rec_sids.append(fid)
                        if fid not in sed_ids: sed_ids.append(fid)
                elif isinstance(p, str):
                    for hit in fpat.finditer(p):
                        fid = hit.group(1)
                        if fid not in rec_fids: rec_fids.append(fid)
                        if fid not in final_ids: final_ids.append(fid)

            tool_records.append({
                "mid": mid,
                "create_time": msg.get("create_time") or 0,
                "model_slug": meta.get("model_slug") or author.get("metadata", {}).get("model_slug"),
                "recipient":  msg.get("recipient") or meta.get("recipient"),
                "author_name": author.get("name"),
                "image_gen_title": meta.get("image_gen_title"),
                "gizmo_id": meta.get("gizmo_id"),
                "file_ids": rec_fids,
                "sed_ids":  rec_sids,
            })

        # 按 create_time 排序（最早 → 最新）
        tool_records.sort(key=lambda r: r["create_time"] or 0)
        last_sed = sed_ids

        # 最终高清直出（优先）—— file-service 一出就是 IMG2 终稿（灰度桶）
        if final_ids:
            for rec in tool_records:
                if rec["file_ids"]:
                    log(f"  [IMG2-final] mid={rec['mid'][:8]} "
                        f"model={rec['model_slug']} recipient={rec['recipient']} "
                        f"name={rec['author_name']} → file-service x{len(rec['file_ids'])}", "OK")
            # file-service 直出一定是灰度 IMG2
            return ("img2", final_ids)

        # 本次回合还没出现任何 tool 消息时继续等（复用会话场景尤其重要）
        if not seen_any_tool:
            log("  等待本次回合的 tool 消息出现...")
            time.sleep(interval)
            continue

        elapsed = time.time() - t0
        n_tool = len(tool_records)
        # 记录第 1 条 tool 消息首次出现时间
        if n_tool >= 1 and first_tool_ts is None:
            first_tool_ts = time.time()
            log(f"  ▶ 第 1 条 tool 消息首次出现，开始 {preview_wait_secs:.0f}s 窗口等第 2 条（IMG2）")

        sig = tuple(sorted(sed_ids))

        # ── 分支 A：已经有 2+ 条 tool 消息 → 灰度命中，按 stable_rounds 等最新那条稳定
        if n_tool >= 2:
            if sed_ids and sig == last_sed_sig:
                stable_count += 1
                log(f"  sed 稳定 {stable_count}/{stable_rounds} "
                    f"（IMG2, tool x{n_tool}, sed x{len(sed_ids)}, {elapsed:.0f}s）")
                if stable_count >= stable_rounds:
                    keep = tool_records[-1]
                    log(f"  ✓ 命中灰度，{n_tool} 条 tool → 只保留最新一条（IMG2 终稿）", "OK")
                    for i, r in enumerate(tool_records[:-1]):
                        log(f"    [丢弃预览 #{i+1}] mid={r['mid'][:8]} t={r['create_time']} "
                            f"name={r['author_name']} sed={len(r['sed_ids'])} file={len(r['file_ids'])}")
                    log(f"    [保留最新  ] mid={keep['mid'][:8]} t={keep['create_time']} "
                        f"name={keep['author_name']} sed={len(keep['sed_ids'])} file={len(keep['file_ids'])}")
                    out = []
                    for fid in keep["file_ids"]:
                        log(f"  最终图片: file-service://{fid[:20]}...", "OK")
                        out.append(fid)
                    for fid in keep["sed_ids"]:
                        log(f"  最终图片: sediment://{fid[:20]}...", "OK")
                        out.append("sed:" + fid)
                    return ("img2", out)
            else:
                if sig != last_sed_sig:
                    log(f"  sed 变化: {len(sed_ids)}张 (已等 {elapsed:.0f}s, tool x{n_tool})")
                stable_count = 0
                last_sed_sig = sig

        # ── 分支 B：只有 1 条 tool 消息 → 看时间窗口，窗口内若冒出第 2 条就走分支 A
        elif n_tool == 1 and first_tool_ts is not None:
            since_first = time.time() - first_tool_ts
            log(f"  窗口中等 IMG2 第 2 条 tool: {since_first:.0f}/{preview_wait_secs:.0f}s "
                f"(sed x{len(sed_ids)}, {elapsed:.0f}s)")
            if since_first >= preview_wait_secs:
                only = tool_records[0]
                log(f"  ✗ 第 1 条 tool 出现 {since_first:.0f}s 后仍无第 2 条 "
                    f"(mid={only['mid'][:8]} name={only['author_name']}) "
                    f"→ 判定非灰度预览，需重试", "ERR")
                return ("preview_only", [])

        time.sleep(interval)

    # 超时兜底
    n_tool = len(tool_records)
    if n_tool >= 2:
        keep = tool_records[-1]
        log(f"  超时但已有 {n_tool} 条 tool 消息，取最新一条作 IMG2", "ERR")
        out = ([x for x in keep["file_ids"]] +
               ["sed:" + x for x in keep["sed_ids"]])
        return ("img2", out)
    if last_sed:
        log(f"  超时，只有 1 条 tool 消息 → 视为非灰度预览（不保存），请重试", "ERR")
        return ("preview_only", [])
    log("  轮询超时，未发现图片", "ERR")
    return ("timeout", [])

# ── Step 5: 下载图片 ─────────────────────────────────────────────────────────
def download_images(s: Session, did: str, conv_id: Optional[str], file_ids: list) -> list:
    os.makedirs(OUTPUT_DIR, exist_ok=True)
    hdrs = {"Authorization": f"Bearer {AUTH_TOKEN}", "oai-device-id": did}
    saved = []

    for raw in file_ids[:8]:
        is_sed = raw.startswith("sed:")
        fid = raw[4:] if is_sed else raw
        if is_sed and conv_id:
            url = f"{BASE_URL}/backend-api/conversation/{conv_id}/attachment/{fid}/download"
        else:
            url = f"{BASE_URL}/backend-api/files/{fid}/download"
        try:
            r = s.get(url, headers=hdrs, timeout=30)
            if not r.ok:
                log(f"  {fid[:16]}... 取下载地址失败 {r.status_code}", "ERR")
                continue
            durl = r.json().get("download_url")
            if not durl:
                log(f"  {fid[:16]}... 无 download_url", "ERR")
                continue
            img = s.get(durl, timeout=60)
            if img.ok and len(img.content) > 2000:
                path = os.path.join(OUTPUT_DIR, f"{fid[-12:]}.png")
                with open(path, "wb") as f:
                    f.write(img.content)
                log(f"已保存: {path} ({len(img.content)//1024} KB)", "OK")
                saved.append(path)
        except Exception as e:
            log(f"  下载 {fid[:16]}... 出错: {e}", "ERR")
    return saved

# ── 单次执行 ─────────────────────────────────────────────────────────────────
def run_once(prompt: str):
    """执行一次完整流程。返回 (status, files)。
    status ∈ {"img2", "preview_only", "timeout", "error"}
    """
    active_proxy = PROXY
    if not active_proxy and PROXY_TEMPLATE:
        active_proxy = _probe_proxy()
        if not active_proxy:
            log("无可用代理出口", "ERR")
            return ("error", [])
    s = new_session(active_proxy)

    try:
        did = bootstrap(s)
        chat_token, pow_info = get_chat_requirements(s, did)

        proof_token = None
        if pow_info.get("required"):
            log("本地计算 proof-of-work...")
            t0 = time.time()
            proof_token = generate_proof_token(
                required=True,
                seed=pow_info["seed"],
                difficulty=pow_info["difficulty"],
                user_agent=UA,
                proof_config=_pow_config(UA),
            )
            log(f"  proof_token len={len(proof_token or '')}  耗时 {time.time()-t0:.2f}s", "OK")

        baseline_tool_ids: set = set()
        if FIXED_CONVERSATION_ID:
            head = get_conversation_head(s, did, FIXED_CONVERSATION_ID)
            if not head:
                # 拉固定会话 head 失败（多为 429 限流），直接本次失败让外层退避重试，
                # 千万不要降级到一个全新的废弃 uuid —— 那会让 /f/conversation 直接 404。
                log("复用会话拉 head 失败 → 本次尝试中止，交由外层退避重试", "ERR")
                return ("error", [])
            conv_id = FIXED_CONVERSATION_ID
            par_id  = head
            log(f"复用会话 {conv_id}  parent={par_id}", "OK")
            baseline_tool_ids = fetch_tool_baseline(s, did, conv_id)
        else:
            # 新会话：必须先 /conversation/init 注册，否则 /f/conversation 会 404
            if not init_new_conversation(s, did):
                return ("error", [])
            conv_id = str(uuid.uuid4())
            par_id  = str(uuid.uuid4())
            log(f"新建会话 {conv_id}  parent={par_id}", "OK")
        msg_id  = str(uuid.uuid4())

        conduit_token = prepare_fconversation(
            s, did, chat_token, proof_token, conv_id, par_id, msg_id, prompt
        )

        t0 = time.time()
        resp = send_conversation(
            s, did, chat_token, proof_token, conv_id, par_id, msg_id, prompt,
            conduit_token=conduit_token,
        )
        result = parse_sse(resp)
        log(f"SSE 总耗时 {time.time()-t0:.1f}s")

        actual_conv = result.get("conversation_id") or conv_id
        sse_ids = result.get("file_ids", [])
        has_final = any(not x.startswith("sed:") for x in sse_ids)

        if actual_conv and not has_final:
            status, polled = poll_conversation_for_images(
                s, did, actual_conv,
                baseline_tool_ids=baseline_tool_ids,
            )
            file_ids = polled
        elif has_final:
            status = "img2"  # file-service 直出就是灰度
            file_ids = sse_ids
        else:
            status = "preview_only"
            file_ids = []

        if status != "img2":
            return (status, [])

        files = download_images(s, did, actual_conv, file_ids)
        return ("img2", files)
    except Exception as e:
        log(f"异常: {e}", "ERR")
        import traceback
        traceback.print_exc()
        return ("error", [])


# ── Main ─────────────────────────────────────────────────────────────────────
def main():
    prompt = " ".join(sys.argv[1:]) if len(sys.argv) > 1 else ""
    if not prompt:
        try:
            prompt = input("请输入提示词: ").strip()
        except EOFError:
            prompt = ""
    if not prompt:
        prompt = "一只可爱的橘猫坐在窗台上，阳光照进来，写实风格"
    if needs_text_rendering(prompt) and CLARITY_SUFFIX.strip() not in prompt:
        prompt += CLARITY_SUFFIX
        log("检测到文字渲染需求 → 已追加文字清晰度约束")
    else:
        log("纯画面提示词 → 跳过文字清晰度 suffix")

    print(f"\n{'='*64}")
    log(f"提示词: {prompt[:100]}{'...' if len(prompt)>100 else ''}")
    log(f"代理: {PROXY.split('@')[-1] if PROXY else '本机默认路由' if not PROXY_TEMPLATE else '自动抽出口'}")
    print(f"{'='*64}\n")

    t_all = time.time()
    for attempt in range(1, MAX_ATTEMPTS + 1):
        print(f"\n{'─'*64}")
        log(f"◆ 尝试 {attempt}/{MAX_ATTEMPTS}")
        print(f"{'─'*64}")
        t0 = time.time()
        status, files = run_once(prompt)
        dt = time.time() - t0

        if status == "img2":
            print(f"\n{'='*64}")
            log(f"✓ 命中灰度（IMG2），{len(files)} 张 → {OUTPUT_DIR}", "OK")
            log(f"  本次 {dt:.0f}s，总计 {time.time()-t_all:.0f}s（用了 {attempt} 次尝试）")
            print(f"{'='*64}\n")
            return

        reason = {"preview_only": "非灰度快速预览（只 1 条 tool 消息）",
                  "timeout":      "轮询超时",
                  "error":        "出错（多为账号级 429 限流）"}[status]
        # 账号级 429 恢复慢：preview_only 退避 40s，error（429）退避 90s 指数上升
        if status == "error":
            backoff = min(180, 60 + attempt * 15)
        else:
            backoff = 40
        if attempt < MAX_ATTEMPTS:
            log(f"× 第 {attempt} 次: {reason}（{dt:.0f}s），等 {backoff}s 冷却后开新 session 重试", "ERR")
            time.sleep(backoff)
        else:
            log(f"× 第 {attempt} 次: {reason}（{dt:.0f}s），已达上限", "ERR")

    print(f"\n{'='*64}")
    log(f"全部 {MAX_ATTEMPTS} 次尝试都未命中灰度，总计 {time.time()-t_all:.0f}s", "ERR")
    print(f"{'='*64}\n")

if __name__ == "__main__":
    main()
