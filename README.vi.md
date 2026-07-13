# 🤚 Swatter

[English](README.md) · **Tiếng Việt**

**Con bugbot review PR, đập bug trước khi nó kịp lọt vào** — findings đã được
kiểm chứng (ít nhiễu) cùng một cuốn rule book *sống*, dựng trên
[agentcore](https://github.com/lohi-ai/agentray). BYOK: mang key Anthropic của
bạn, hoặc trỏ vào bất kỳ gateway nào tương thích OpenAI (9router, OpenRouter,
LiteLLM, Ollama). Open source, self-hosted ngay trong CI của bạn — không data
nào rời khỏi runner trừ mấy cú gọi model bạn tự cấu hình.

## Sao lại thêm một con reviewer nữa?

Đa số con reviewer AI chạy **một lượt duy nhất** rồi post nguyên si những gì
model phun ra — nhiễu là lời than số một. Swatter thì chạy pipeline
tìm-rồi-kiểm-chứng:

1. **Finders** — tối đa tám góc nhìn độc lập (dò từng dòng, hành vi bị xóa,
   cross-file, security, dọn dẹp, quy ước, tính tuân thủ, nhất-quán-pattern) đọc
   *file thật*, không chỉ mỗi diff.
2. **Validators** — mọi candidate CRITICAL/MAJOR đều bị một agent *mới toanh*
   soi lại, con này chưa từng thấy lập luận của finder và buộc phải lần theo
   code path thật. Lý lẽ suy đoán thì loại; giữ lại cái nào chứng minh được.
3. **Một cuốn rule book sống** (`.swatter/rules.md`) — findings đã xác nhận sẽ
   dạy ra rules; cuốn sách tự dedup, chấm điểm theo hit/miss, và cho hết hạn mấy
   entry cũ mèm, nên con bot ngày càng sắc bén trên chính codebase *của bạn*
   ([hoạt động thế nào](docs/DESIGN-RULEBOOK.vi.md)).

## Quickstart

> **Mới dùng Swatter? Chưa cần CI để thử đâu.** Chạy nguyên một lượt review
> ngay trên máy trước — xem [CLI standalone](#cli-standalone-thử-trước-khi-gắn-vào-ci)
> bên dưới — rồi hẵng dựng Action khi đã ưng.

```bash
# in your repo, with the GitHub CLI authenticated:
swatter init          # asks provider/model + review trigger, writes the workflow, sets the secret
```

`init` sẽ hỏi bạn muốn trigger review kiểu nào:

- **per-commit** (mặc định) — review mỗi lần push. Liên tục, nhưng trả tiền cho
  một lượt review đầy đủ mỗi commit. (`swatter init --mode per-commit`)
- **on-demand** — review khi PR mở, sau đó chỉ khi một maintainer comment
  `@swatter review`. Không chạy theo từng commit, nên tốn ít token hơn hẳn với
  mấy PR đẩy commit liên tục. (`swatter init --mode on-demand`)

…hoặc thêm `.github/workflows/swatter.yml` bằng tay:

```yaml
name: swatter
on:
  pull_request:
    types: [opened, synchronize, reopened]
concurrency:
  group: swatter-${{ github.event.pull_request.number }}
  cancel-in-progress: true
permissions:
  contents: read
  pull-requests: write
  checks: write
jobs:
  review:
    # Chỉ PR cùng repo. Trên public repo, PR từ fork chỉ có token read-only và
    # không có secrets nên auto-review không post được — xem docs/recipes.md để
    # review PR fork theo yêu cầu.
    if: github.event.pull_request.head.repo.full_name == github.repository
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
        with: { fetch-depth: 0 }        # full history for base...head diff
      - uses: lohi-ai/swatter@v0
        with:
          api_key: ${{ secrets.SWATTER_API_KEY }}
          model: claude-opus-4-8
```

Mở một PR — Swatter post comment inline, một comment tổng kết, và một check run
**Swatter**. Thêm nhiều pattern nữa (gateway, re-trigger `@swatter review`, lọc
theo path, chế độ advisory, an toàn với fork-PR) trong
[docs/recipes.vi.md](docs/recipes.vi.md).

## CLI standalone (thử trước khi gắn vào CI)

Muốn xem Swatter chạy trên code của bạn trước khi dựng workflow? Chạy review
ngay tại máy. Lưu key một lần, kiểm tra provider có trả lời không, rồi review
nhánh hiện tại:

```bash
swatter config set api-key sk-…       # lưu 0600 vào ~/.config/swatter/config.json
swatter doctor                        # kiểm tra config, git, GitHub token + một lần gọi model rẻ
swatter review                        # review nhánh hiện tại so với nhánh mặc định → stdout
swatter review high                   # ép mức effort (auto|low|medium|high|xhigh|max)
swatter review main..HEAD             # review một range git cụ thể (three-dot / merge-base)
swatter review low --comment 42       # review và post finding lên PR #42 (cần GitHub token)
```

- **`swatter config set|get|list|path`** quản lý `~/.config/swatter/config.json`
  (tôn trọng `$XDG_CONFIG_HOME`) để bạn khỏi export `SWATTER_*` bằng tay. Các
  key: `api-key`, `provider`, `base-url`, `model`, `model-cheap`, `effort`,
  `fail-on`, `github-token`, `resolve-token`. File được xếp **dưới** environment
  — biến `SWATTER_*` đã set luôn thắng — nên CI (vốn set env và không có file)
  không bị ảnh hưởng. `config list` che các giá trị bí mật.
- **`swatter doctor`** validate config, kiểm tra ngữ cảnh git và (nếu có token)
  quyền GitHub, rồi gọi model một lần thật nhỏ để một key/gateway sai sẽ báo lỗi
  sớm thay vì giữa chừng. `--no-llm` để bỏ qua lần gọi đó.
- **`swatter review [effort] [--comment] [<target>]`** chạy đúng pipeline
  find-then-verify như CI. `<target>` để trống (nhánh hiện tại so với merge-base
  với nhánh mặc định), một ref/range git, hoặc số/URL của PR. Không có
  `--comment` thì finding in ra stdout (`--format json` cho output máy đọc).
  `--comment` post lên PR y như CI — hãy checkout nhánh của PR trước để comment
  inline neo đúng commit, và set GitHub token (`swatter config set github-token …`
  hoặc `GITHUB_TOKEN`).

`run`/`learn`/`init` và GitHub Action giữ nguyên — CLI chỉ là một cửa vào mới
trên cùng một engine, không phải bản thay thế.

## Configuration

| Input | Mặc định | Ghi chú |
|---|---|---|
| `api_key` | — (bắt buộc) | Key BYOK; cất làm secret. |
| `provider` | `anthropic` | hoặc `openai-compat`. |
| `base_url` | — | bắt buộc với `openai-compat`. |
| `model` | `claude-opus-4-8`\* | tier mạnh (góc bug/security, diff lớn). |
| `model_cheap` | = `model` | tier rẻ hơn cho mấy góc dọn dẹp trên diff nhỏ. |
| `effort` | `high` | mức review: `low` (1 lượt diff → không verify → ≤4 findings), `medium` (3+5 góc × 6 → verify → ≤8, thiên precision), `high` (cùng fan-out, thiên recall → ≤10), `xhigh` (5+5 góc × 8 → verify → quét → ≤15), `max` (xhigh + reasoning effort của API). Mỗi mức còn hard-cap token cho từng agent — `high` giữ mỗi agent dưới 120K. |
| `fail_on` | `never` | mặc định là advisory (check xanh + comment). Đặt `critical`/`major`/`any` để chặn merge — check `Swatter` chuyển đỏ khi có finding đã xác nhận. |
| `max_usd` | `5` | trần chi tiêu mỗi PR (với model có giá). |
| `max_tokens_total` | `8000000` | trần lúc nào cũng chặn được, cho model không rõ giá. |
| `price_per_mtok_in`/`_out` | `0` | dạy cho ledger biết giá của một model tự chọn. |

\* Không có mặc định cho `openai-compat` — tự đặt tên model của gateway.

## Safety

Swatter chạy trên nội dung PR không đáng tin (diff, mô tả có thể do kẻ tấn công
nhét vào trên public repo). Mấy agent review đều **read-only** — không shell,
không network tool, không GitHub token. Findings là JSON có kiểu, do harness
render ra; harness mới là chỗ giữ token và lo hết phần post. Một chỉ thị lén
nhét vào body của PR không thể bắt con bot post, tuồn data, hay chạy bất cứ thứ
gì.

## Development

```bash
go build ./...
go test ./...                    # deterministic unit tests
SWATTER_LIVE_TEST=1 SWATTER_API_KEY=… SWATTER_MODEL=… \
  go test ./internal -run TestPipelineFixture   # live fixture replay
```

Swatter xài agentcore từ
[`github.com/lohi-ai/agentray`](https://github.com/lohi-ai/agentray), ghim trong
`go.mod` và lấy về từ module proxy — không cần setup gì thêm để build. Muốn vọc
agentcore và Swatter cùng lúc, thêm một dòng
`replace github.com/lohi-ai/agentray => ../agentray` trỏ vào một checkout nằm
cạnh (và nhớ bỏ nó trước khi commit).

## License

[Apache-2.0](LICENSE).
