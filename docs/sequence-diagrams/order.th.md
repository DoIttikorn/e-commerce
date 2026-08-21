# Order — UML Sequence Diagram

*English: [order.md](order.md) · สารบัญ: [README.th.md](README.th.md)*

Saga — การสั่งซื้อพาดผ่านสอง service ที่มีคนละ database จึง **ไม่มี transaction ให้ใช้**
อะไรก็ตามที่พาดผ่านทั้งสองตัว จะเป็นลำดับของขั้นตอนในเครื่องแต่ละตัว
บวกกับการชดเชยไว้เผื่อขั้นตอนหลังพัง

| Flow | Endpoint |
|---|---|
| ⭐⭐ [การสั่งซื้อ](#-การสั่งซื้อ--saga) | `POST /api/v1/orders` |
| ⭐ [การส่ง idempotency key ซ้ำ](#-การส่ง-idempotency-key-ซ้ำ) | `POST /api/v1/orders` |
| [รายการและรายตัว](#รายการและรายตัว) | `GET /api/v1/orders`, `GET /api/v1/orders/{id}` |
| [ชำระเงิน](#ชำระเงิน) | `POST /api/v1/orders/{id}/pay` |
| ⭐ [ยกเลิก](#-ยกเลิก--compensating-action) | `POST /api/v1/orders/{id}/cancel` |

---

## ⭐⭐ การสั่งซื้อ — saga

ทั้งระบบอยู่ใน request เดียว และเป็นรูปที่ควรอ่านสองรอบ

**ทำไม call นี้ถึงต้อง synchronous** ทุกการติดต่อข้าม service ที่เหลือในระบบนี้เป็น event
เพราะที่เหลือรอได้ แต่อันนี้รอไม่ได้ — จะบอกผู้ซื้อว่า order สำเร็จไม่ได้
จนกว่าจะจอง stock ได้จริง

**ทำไม `Confirm` พังแล้วไม่ทำให้ request พัง** เพราะถึงตอนนั้น order ถูกเขียนแล้ว
และผู้ซื้อได้รับคำตอบไปแล้ว การจองแค่ยังไม่ถูก confirm เท่านั้น
และช่วงผ่อนผันของ reaper นานพอที่การ retry หรือการ restart จะแก้ได้
ถ้าทำให้ request พังตรงนี้ เท่ากับรายงานความสำเร็จว่าเป็นความล้มเหลว

**ทำไมการชดเชยเป็นแบบ best-effort** เพราะถ้า `Release` พังด้วย reaper
ก็ยังยึดสต็อกคืนอยู่ดี กลไกสองอันที่เป็นอิสระจากกันคุ้มครองช่องว่างเดียวกัน
เพราะความล้มเหลวที่แพงที่สุด — สต็อกถูกยึดไว้โดยไม่มี order ตลอดกาล — คุ้มที่จะกันสองชั้น

```mermaid
sequenceDiagram
    autonumber
    actor C as ผู้ซื้อ
    participant H as order handler
    participant S as order.Service
    participant Cl as grpcstock client
    participant P as Product service
    participant R as mongodb adapter
    participant DB as MongoDB ของ order
    participant RL as outbox relay
    participant K as Kafka

    C->>H: POST /api/v1/orders พร้อม Idempotency-Key
    alt ไม่มี header
        H-->>C: 400 — key คือสิ่งที่ทำให้การ retry ปลอดภัย
    else มี
        H->>H: ตรวจความถูกต้องของรายการสินค้า
        H->>S: Place(ctx, buyerID, items, key)

        rect rgb(255, 250, 235)
            Note over S,P: ขั้นที่ 1 — จองสต็อก ผ่าน gRPC และ mutual TLS
            S->>Cl: Reserve(ctx, key, lines)
            Cl->>P: StockService/Reserve
            alt สต็อกไม่พอ หรือไม่มีสินค้านี้
                P-->>Cl: FailedPrecondition หรือ NotFound
                Cl-->>S: error ที่แปลงเป็น domain error แล้ว
                S-->>H: error
                H-->>C: 409 หรือ 404 — ไม่มี order ถูกสร้าง
            else จองได้
                P-->>Cl: รายการที่จองได้ พร้อมราคาที่เชื่อถือได้
                Cl-->>S: รายการที่จองได้
                Note over S,Cl: ราคามาจาก Product ไม่ใช่จาก client<br/>ราคาที่ client ส่งมาเอง<br/>คือส่วนลดที่ client ให้ตัวเอง
            end
        end

        rect rgb(240, 255, 240)
            Note over S,DB: ขั้นที่ 2 — เขียน order และ event ของมันไปพร้อมกัน
            S->>S: รวมยอดจากรายการที่จองได้ เป็นหน่วยย่อยล้วน
            S->>R: Save(ctx, Order, events)
            R->>DB: transaction — insert order และ insert outbox
            alt เขียนไม่สำเร็จ
                DB-->>R: abort
                R-->>S: error
                rect rgb(255, 235, 235)
                    Note over S,P: ชดเชย — สต็อกถูกยึดไว้<br/>ให้กับ order ที่ไม่มีอยู่จริง
                    S->>Cl: Release(ctx, key)
                    Cl->>P: ปล่อยการจอง
                    Note over S,P: best effort ถ้าอันนี้พังด้วย<br/>reaper ก็ยังยึดคืนอยู่ดี
                end
                S-->>H: error
                H-->>C: 500 — และไม่มีสต็อกค้างเปล่า
            else commit สำเร็จ
                DB-->>R: commit
                R-->>S: Order
            end
        end

        rect rgb(240, 248, 255)
            Note over S,P: ขั้นที่ 3 — confirm เพื่อให้ reaper ไม่มายุ่ง
            S->>Cl: Confirm(ctx, key)
            Cl->>P: StockService/Confirm
            alt confirm ไม่สำเร็จ
                Note over S,Cl: log ไว้ ไม่คืนเป็น error — order มีอยู่จริง<br/>และผู้ซื้อได้รับคำตอบไปแล้ว<br/>ช่วงผ่อนผันของ reaper ครอบคลุมส่วนที่เหลือ
            end
        end

        S-->>H: Order
        H-->>C: 201 order สถานะ pending
    end

    Note over C,H: request จบแล้ว

    RL->>DB: จองแถวใน outbox
    RL->>K: publish order.events โดย key = order id
    RL->>DB: บันทึก published_at
    Note over RL,K: Marketplace consume อันนี้ไปอัปเดต sold_count<br/>โดย Order ไม่รู้จัก Marketplace เลย
```

## ⭐ การส่ง idempotency key ซ้ำ

คือสิ่งที่ client ทำเวลา retry หลัง timeout — caller ไม่มีทางรู้ว่า server ทำไปแล้วหรือยัง
มันจึงจะ retry และ key คือสิ่งที่ทำให้การ retry นั้นปลอดภัย ไม่ให้ "จอง stock"
กลายเป็น "จอง stock สองรอบ"

```mermaid
sequenceDiagram
    autonumber
    actor C as ผู้ซื้อ
    participant S as order.Service
    participant Cl as grpcstock client
    participant P as Product service
    participant R as mongodb adapter
    participant DB as MongoDB ของ order

    Note over C: ครั้งแรก timeout ไป<br/>server ทำไปแล้วหรือยัง ไม่มีทางรู้
    C->>S: POST /api/v1/orders ด้วย Idempotency-Key เดิม
    S->>R: หา order ที่เคยเขียนด้วย key นี้
    alt มี order อยู่แล้ว
        R->>DB: findOne ด้วย idempotency key
        DB-->>R: order ใบเดิม
        R-->>S: Order
        S-->>C: 200 พร้อม order ใบ**เดิม** ไม่มีการเขียนรอบสอง
    else ยังไม่มี order
        S->>Cl: Reserve(ctx, key, lines)
        Cl->>P: StockService/Reserve
        P->>P: key นี้อยู่ใน stock_reservations แล้ว
        P-->>Cl: รายการที่จองได้ **ชุดเดิม** กับครั้งแรก
        Note over P,Cl: สต็อกถูกหักครั้งเดียว ไม่ใช่สองครั้ง<br/>ถ้าไม่มี key เน็ตที่ไม่เสถียร<br/>จะกลายเป็นลูกค้าโดนตัดเงินซ้ำ
        Cl-->>S: รายการที่จองได้
        S->>R: Save — รอบนี้ order ถูกเขียนสำเร็จ
        S-->>C: 201 order
    end
```

## รายการและรายตัว

```mermaid
sequenceDiagram
    autonumber
    actor C as ผู้ซื้อ
    participant MW as auth middleware
    participant H as order handler
    participant S as order.Service
    participant R as mongodb adapter

    C->>MW: GET /api/v1/orders หรือ /api/v1/orders/{id}
    MW->>H: subject อยู่บน context
    alt ขอเป็นรายการ
        H->>S: ListForBuyer(ctx, subject, limit, offset)
        Note over H,S: จำกัดตาม subject เท่านั้น ไม่มี endpoint<br/>"order ทั้งหมด" การไม่ใส่ filter<br/>จึงขยายผลลัพธ์ไม่ได้
        S->>R: หาโดย buyer_user_id
        R-->>S: orders และ total
        H-->>C: 200 orders, total, limit, offset
    else ขอรายตัว
        H->>S: ByID(ctx, id)
        S->>R: ByID
        alt id ผิดรูปแบบ
            H-->>C: 400 malformed order id
        else ไม่มีของ
            H-->>C: 404 order not found
        else เป็นของผู้ซื้อคนอื่น
            H-->>C: 403 forbidden
        else เป็นของตัวเอง
            H-->>C: 200 order
        end
    end
```

## ชำระเงิน

สต็อกที่จองไว้ยังคงถูกจองต่อไป เพราะการขายสำเร็จแล้ว มี state machine คุมการเปลี่ยนสถานะ
การจ่ายเงินให้ order ที่ยกเลิกไปแล้วจึงเป็น conflict ไม่ใช่การแก้ไขให้

```mermaid
sequenceDiagram
    autonumber
    actor C as ผู้ซื้อ
    participant H as order handler
    participant S as order.Service
    participant R as mongodb adapter
    participant DB as MongoDB ของ order

    C->>H: POST /api/v1/orders/{id}/pay
    H->>S: Pay(ctx, id, buyerID)
    S->>R: ByID(ctx, id)
    alt ไม่ใช่ผู้ซื้อ
        S-->>H: ErrNotBuyer
        H-->>C: 403 forbidden
    else เป็นผู้ซื้อ
        alt สถานะไม่ใช่ pending
            S-->>H: ErrInvalidTransition
            H-->>C: 409 — order ที่จ่ายแล้วหรือยกเลิกแล้ว จ่ายซ้ำไม่ได้
        else pending
            S->>R: Save สถานะ paid พร้อม event order.paid ลง outbox
            R->>DB: transaction — update order และ insert outbox
            DB-->>R: commit
            S-->>H: Order
            H-->>C: 200 order สถานะ paid
        end
    end
```

## ⭐ ยกเลิก — compensating action

ปลายอีกด้านของ saga การยกเลิกจะปล่อยการจอง แล้วสต็อกกลับคืน

**การชดเชยต้องเรียกซ้ำได้อย่างปลอดภัย** เพราะมันทำงานบนเส้นทางที่มีการ retry อยู่แล้ว
การปล่อย key ที่ไม่รู้จัก หรือ key ที่ปล่อยไปแล้ว จะคืน success ไม่ใช่ error
ถ้าปฏิเสธการเรียกซ้ำตรงนี้ สถานการณ์ที่กู้คืนได้จะกลายเป็นค้างถาวร

```mermaid
sequenceDiagram
    autonumber
    actor C as ผู้ซื้อ
    participant H as order handler
    participant S as order.Service
    participant R as mongodb adapter
    participant DB as MongoDB ของ order
    participant Cl as grpcstock client
    participant P as Product service
    participant PD as MongoDB ของ product

    C->>H: POST /api/v1/orders/{id}/cancel
    H->>S: Cancel(ctx, id, buyerID)
    S->>R: ByID(ctx, id)
    alt ไม่ใช่ผู้ซื้อ
        H-->>C: 403 forbidden
    else สถานะไม่ใช่ pending
        H-->>C: 409 — จ่ายไปแล้วหรือยกเลิกไปแล้ว
    else pending และเป็นของตัวเอง
        S->>R: Save สถานะ cancelled พร้อม event order.cancelled
        R->>DB: transaction — update order และ insert outbox
        DB-->>R: commit

        S->>Cl: Release(ctx, idempotency key)
        Cl->>P: ปล่อยการจอง
        P->>PD: findOneAndUpdate {_id, released:false} ตั้ง released=true
        alt key ไม่รู้จัก หรือปล่อยไปแล้ว
            PD-->>P: ไม่มี document
            P-->>Cl: คืน success อยู่ดี
            Note over P,Cl: idempotent โดยตั้งใจ อันนี้ทำงานบนเส้นทาง<br/>ที่มีการ retry การปฏิเสธการเรียกซ้ำ<br/>จะเปลี่ยนสิ่งที่กู้คืนได้ให้กลายเป็นค้างถาวร
        else จองสิทธิ์ได้
            loop ทุกรายการที่จองไว้
                P->>PD: $inc stock เพิ่ม +quantity
            end
            P-->>Cl: success
        end
        Cl-->>S: สำเร็จ
        S-->>H: Order
        H-->>C: 200 order สถานะ cancelled
    end
```
