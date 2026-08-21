# Seller — UML Sequence Diagram

*English: [seller.md](seller.md) · สารบัญ: [README.th.md](README.th.md)*

ร้านค้า — ทุกการเขียนในนี้จะ publish event ที่ Product และ Marketplace consume
นี่คือจุดเริ่มต้นของฝั่ง event-driven ของระบบ

| Flow | Endpoint |
|---|---|
| [เปิดร้าน](#เปิดร้าน) | `POST /api/v1/sellers` |
| [ร้านของฉัน](#ร้านของฉัน) | `GET /api/v1/sellers/me` |
| [รายการและรายตัว](#รายการและรายตัว) | `GET /api/v1/sellers`, `GET /api/v1/sellers/{id}` |
| ⭐ [เปลี่ยนชื่อร้าน](#-เปลี่ยนชื่อร้าน--รูปที่ควรลองรันจริง) | `PATCH /api/v1/sellers/{id}` |

---

## เปิดร้าน

row ของร้าน กับ event ของมัน ถูกเขียนใน **transaction เดียวกัน** และ event
ไม่ได้ถูก publish ตรงนี้ — มี relay เบื้องหลังมาทำทีหลัง
เพราะการ publish จาก request path จะเหลือช่องว่างไว้: write commit สำเร็จ
แต่ publish พัง แล้วร้านมีอยู่จริงโดยไม่มีใครรู้ และการคืน error ก็ไม่ช่วย เพราะ write เกิดไปแล้ว

```mermaid
sequenceDiagram
    autonumber
    actor C as ผู้ใช้
    participant MW as auth middleware
    participant H as handler
    participant S as seller.Service
    participant R as mongodb adapter
    participant DB as MongoDB

    C->>MW: POST /api/v1/sellers พร้อม bearer token
    MW->>H: subject อยู่บน context
    H->>H: ตรวจ shop_name และตัดช่องว่างหัวท้าย
    alt ไม่ผ่าน
        H-->>C: 400 validation failed พร้อม fields
    else ผ่าน
        H->>S: Register(ctx, NewSeller)
        S->>S: ประกอบ event seller.registered
        S->>R: Create(ctx, Seller, events)
        R->>DB: เริ่ม transaction
        R->>DB: insertOne ที่ sellers
        R->>DB: insertMany ที่ outbox
        alt มีอันใดอันหนึ่งพัง
            DB-->>R: abort ไม่มีอะไรถูก commit
            R-->>S: error
            H-->>C: 409 หรือ 500 และไม่มี event เกิดขึ้น
        else สำเร็จทั้งคู่
            DB-->>R: commit
            R-->>S: Seller
            S-->>H: Seller
            H-->>C: 201 seller สถานะ active
        end
    end
    Note over R,DB: ตอนนี้ event คงทนแล้วแต่ยังไม่ถูก publish<br/>ดู relay ที่มาเก็บไปได้ที่ cross-cutting.th.md
```

## ร้านของฉัน

หาร้านจาก subject ของ token แทนที่จะรับ ID ทาง path — ไม่มี ID ให้ใส่ผิด
และไม่มีทางถามหาร้านของคนอื่น

```mermaid
sequenceDiagram
    autonumber
    actor C as ผู้ใช้
    participant MW as auth middleware
    participant H as handler
    participant S as seller.Service
    participant R as mongodb adapter

    C->>MW: GET /api/v1/sellers/me
    MW->>H: subject อยู่บน context
    H->>S: ByUserID(ctx, subject)
    S->>R: ByUserID(ctx, userID)
    alt บัญชีนี้ยังไม่มีร้าน
        R-->>S: ErrSellerNotFound
        H-->>C: 404 seller not found
    else เจอ
        R-->>S: Seller
        S-->>H: Seller
        H-->>C: 200 seller
    end
```

## รายการและรายตัว

```mermaid
sequenceDiagram
    autonumber
    actor C as ผู้ใช้
    participant H as handler
    participant S as seller.Service
    participant R as mongodb adapter
    participant DB as MongoDB

    C->>H: GET /api/v1/sellers หรือ /api/v1/sellers/{id}
    alt ขอเป็นรายการ
        H->>S: List(ctx, limit, offset)
        S->>R: List
        R->>DB: find พร้อม countDocuments
        DB-->>R: หน้าข้อมูลและจำนวนรวม
        H-->>C: 200 sellers, total, limit, offset
    else ขอรายตัว
        H->>S: ByID(ctx, id)
        S->>R: ByID
        alt id ผิดรูปแบบ
            H-->>C: 400 malformed seller id
        else ไม่มีของ
            H-->>C: 404 seller not found
        else เจอ
            H-->>C: 200 seller
        end
    end
```

## ⭐ เปลี่ยนชื่อร้าน — รูปที่ควรลองรันจริง

นี่คือรูปที่ทำให้เห็นสถาปัตยกรรมชัดที่สุด การเปลี่ยนชื่อร้านจะไปเปลี่ยนข้อมูลใน
**service อีกสองตัว** โดยไม่มีการเรียก service ทั้งสองนั้นเลยสักครั้ง

Product ไม่เคยเรียก Seller เลย มันสร้าง read model ของตัวเองจาก stream นี้
การ render หน้ารายการสินค้าจึงไม่มี outbound call เลย และถ้า Seller service ล่ม
ก็ไม่ได้ทำให้ดูแคตตาล็อกไม่ได้

event ใช้ seller ID เป็น key เพราะ Kafka เรียงลำดับ **ต่อ partition**
ถ้าเปลี่ยนชื่อร้านเดียวกันสองครั้งแล้วไม่ใช้ key เดียวกัน ลำดับอาจสลับกันได้

```mermaid
sequenceDiagram
    autonumber
    actor C as ผู้ใช้
    participant H as handler
    participant S as seller.Service
    participant R as mongodb adapter
    participant DB as MongoDB
    participant RL as outbox relay
    participant K as Kafka
    participant P as Product service
    participant M as Marketplace service

    C->>H: PATCH /api/v1/sellers/{id}
    H->>H: requireOwner — subject ต้องเป็นเจ้าของร้าน
    alt ไม่ใช่เจ้าของ
        H-->>C: 403 forbidden
    else เป็นเจ้าของ
        H->>S: Update(ctx, id, Update)
        S->>S: ประกอบ seller.updated โดย key = seller id
        S->>R: Update(ctx, id, Update, events)
        R->>DB: transaction — update sellers และ insert outbox
        DB-->>R: commit
        R-->>S: Seller
        H-->>C: 200 seller
    end

    Note over C,H: request จบแล้ว ทุกอย่างข้างล่างนี้<br/>เกิดขึ้นบน background loop

    RL->>DB: จองแถวที่เก่าที่สุดที่ยังไม่ถูก publish
    RL->>K: publish ไปที่ seller.events โดย key = seller id
    RL->>DB: บันทึก published_at
    par consumer ทุกตัวของ topic นี้
        K->>P: seller.updated
        P->>P: upsert ลง seller_directory ในเครื่อง
        P->>P: เขียน seller_name ใหม่บนสินค้าของร้านนั้น
        P->>P: ล้าง cache เฉพาะ key เหล่านั้น ไม่ใช่การ scan
    and
        K->>M: seller.updated
        M->>M: อัปเดต seller_name บน search projection
    end
    Note over P,M: ตอนนี้หน้ารายการของทั้งสอง service แสดงชื่อใหม่แล้ว<br/>โดยไม่มีตัวไหนเคยเรียก Seller เลย
```
