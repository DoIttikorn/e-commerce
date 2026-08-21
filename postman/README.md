# Postman collection

A runnable tour of all six services. **54 requests, 125 assertions, and the whole
thing passes end to end** — it is a working artefact, not a list of URLs.

*ทัวร์ที่รันได้จริงของทั้ง 6 services — **54 requests, 125 assertions และผ่านทั้งหมดตั้งแต่ต้นจนจบ**
เป็นของที่ใช้งานได้จริง ไม่ใช่แค่รายการ URL*

| File | |
|---|---|
| `e-commerce.postman_collection.json` | the requests |
| `e-commerce.postman_environment.json` | where the services are |

---

## Import  ·  การนำเข้า

**EN** — In Postman: **Import** → drop **both** files in → then pick
*e-commerce — local (docker compose)* from the environment dropdown at the top
right. The collection will not resolve `{{userBase}}` without it.

**TH** — ใน Postman: **Import** → ลากไฟล์ **ทั้งสอง** เข้าไป → แล้วเลือก
*e-commerce — local (docker compose)* จาก dropdown ของ environment มุมขวาบน
ถ้าไม่เลือก collection จะแปลค่า `{{userBase}}` ไม่ออก

Start the stack first:

```bash
make docker-run
curl localhost:8080/readyz
```

---

## Run it  ·  วิธีรัน

**EN** — Folder order matters. `01 Auth` fills `{{token}}`; every folder after it
depends on that, and 03 → 04 → 05 each capture the ID the next one needs. Use the
**Collection Runner** on the whole collection and it works unattended — folder 10
is destructive and deliberately last.

**TH** — ลำดับของ folder สำคัญ — `01 Auth` เติมค่า `{{token}}` และทุก folder หลังจากนั้นต้องใช้มัน
ส่วน 03 → 04 → 05 แต่ละอันจะเก็บ ID ที่อันถัดไปต้องใช้ ใช้ **Collection Runner**
รันทั้ง collection ได้เลยโดยไม่ต้องเฝ้า — folder 10 เป็นการลบข้อมูล จึงจงใจวางไว้ท้ายสุด

| Folder | |
|---|---|
| `00` Health & Observability | liveness vs readiness, metrics, and proof pprof is not public |
| `01` Auth | register, login — **run first**, fills `{{token}}` |
| `02` Users | CRUD, and why it is PATCH rather than PUT |
| `03` Seller | open a shop; the rename is the one worth running |
| `04` Product | the catalogue, cached; watch the shop rename arrive here |
| `05` Order | the saga: gRPC over mTLS, idempotency, pay vs cancel |
| `06` Marketplace | search projection fed by three event streams |
| `07` Live | Redis presence and broadcast; one WebSocket note |
| `08` Failure modes | every request here is *supposed* to fail |
| `09` Tracing | send your own `traceparent` and follow it |
| `10` Cleanup | destructive — runs last, safe to skip |

From the command line, if you prefer:

```bash
npx newman run e-commerce.postman_collection.json \
  -e e-commerce.postman_environment.json
```

---

## Two things worth doing by hand  ·  สองอย่างที่ควรลองเอง

**EN**

1. **Run `03 · Rename the shop`, then re-run `04 · List products`.** `seller_name`
   changes on products nobody touched. The Product service never calls the Seller
   service — it keeps its own read model built from that event stream.
2. **Run `05 · Place an order` with Jaeger open** (http://localhost:16686).
   One trace, seven spans, three services — including the outbox publish that
   happened *after* the request had already returned.

**TH**

1. **รัน `03 · Rename the shop` แล้วกลับไปรัน `04 · List products`** — `seller_name`
   จะเปลี่ยนบนสินค้าที่ไม่มีใครไปแตะเลย เพราะ Product service ไม่เคยเรียก Seller service
   แต่สร้าง read model ของตัวเองจาก event stream
2. **รัน `05 · Place an order` โดยเปิด Jaeger ไว้** (http://localhost:16686)
   จะเห็น 1 trace, 7 spans, 3 services — รวมถึง outbox publish ที่เกิดขึ้น
   *หลังจาก* request ตอบกลับไปแล้ว

---

## Notes  ·  ข้อควรรู้

**EN**

- **The environment holds only base URLs.** Token and captured IDs are collection
  variables on purpose: Postman resolves environment scope ahead of collection
  scope, so an environment variable that exists but is empty silently shadows
  whatever a script just wrote. That mistake made every request 401 the first
  time this collection was run.
- **`04 · Wait for the seller event to reach Product`** is a two-second pause, not
  an endpoint. Product learns about shops from Kafka, and nothing exposes that
  read model to poll. Skip it when clicking by hand — you are slower than it
  already. A **409 unknown seller** is that race, not a bug.
- **Money is minor units.** `25000` is ฿250.00. Never a float.
- The WebSocket request in folder 07 cannot run as plain HTTP. Use Postman's
  **WebSocket** request type against
  `ws://localhost:8085/api/v1/live/streams/{{streamId}}/watch`.

**TH**

- **environment เก็บแค่ base URL เท่านั้น** — token และ ID ที่จับมาได้เป็น collection variable
  โดยตั้งใจ เพราะ Postman แปลค่า environment ก่อน collection ตัวแปรใน environment
  ที่มีอยู่แต่ว่างเปล่าจะบังค่าที่ script เพิ่งเขียนลงไปแบบเงียบ ๆ
  ความผิดพลาดนี้ทำให้ทุก request ได้ 401 ตอนรัน collection นี้ครั้งแรก
- **`04 · Wait for the seller event to reach Product`** คือการหน่วง 2 วินาที ไม่ใช่ endpoint จริง
  Product รู้จักร้านค้าจาก Kafka และไม่มีอะไรเปิดเผย read model นั้นให้ poll ได้
  ถ้าคลิกเองข้ามได้เลย เพราะคนช้ากว่านั้นอยู่แล้ว ส่วน **409 unknown seller** คือการแข่งกันของ event
  ไม่ใช่ bug
- **เงินเป็นหน่วยย่อย** — `25000` คือ ฿250.00 ห้ามเป็น float
- request WebSocket ใน folder 07 รันเป็น HTTP ธรรมดาไม่ได้ ให้ใช้ request แบบ
  **WebSocket** ของ Postman ยิงไปที่
  `ws://localhost:8085/api/v1/live/streams/{{streamId}}/watch`

---

The same contract also lives in [`../test/http/api.http`](../test/http/api.http)
(VS Code REST Client) and [`../test/grpc/requests.md`](../test/grpc/requests.md).
Change one, change the others.

*สัญญาชุดเดียวกันนี้อยู่ใน [`../test/http/api.http`](../test/http/api.http)
และ [`../test/grpc/requests.md`](../test/grpc/requests.md) ด้วย — แก้ที่ไหน แก้ให้ครบทุกที่*
