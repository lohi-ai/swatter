# 🤚 Swatter

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
