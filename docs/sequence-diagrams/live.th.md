# Live Commerce — UML Sequence Diagram

*English: [live.md](live.md) · สารบัญ: [README.th.md](README.th.md)*

ไลฟ์ขายของ และทุกการตัดสินใจในการออกแบบมาจากข้อเท็จจริงข้อเดียว:
**WebSocket หนึ่งเส้นอยู่บน instance เดียวเท่านั้น** อะไรก็ตามที่ต้องเป็นจริงสำหรับผู้ชมทุกคน
จึงอยู่ใน memory ของ process ไม่ได้

Redis ที่นี่เป็น **ของบังคับ** ต่างจากใน Product ที่เป็นแค่ cache
ถ้าไม่มี Redis สอง instance จะต่างคนต่างจำว่าใครดูอยู่บ้าง และจะผิดทั้งคู่

| Flow | Endpoint |
|---|---|
| [สร้างไลฟ์](#สร้างไลฟ์) | `POST /api/v1/live/streams` |
| [เริ่มและจบ](#เริ่มและจบ) | `POST .../start`, `POST .../end` |
| ⭐⭐ [เข้าดู — WebSocket](#-เข้าดู--websocket) | `GET .../watch` |
| ⭐ [ปักสินค้า](#-ปักสินค้า--กระจายข้ามอินสแตนซ์) | `POST .../feature` |
| ⭐ [presence ที่ต้องหมดอายุได้](#-presence-ที่ต้องหมดอายุได้) | เบื้องหลัง |
| [รายการและรายตัว](#รายการและรายตัว) | `GET /api/v1/live/streams`, `GET .../{id}` |

---

## สร้างไลฟ์

```mermaid
sequenceDiagram
    autonumber
    actor C as เจ้าของไลฟ์
    participant MW as auth middleware
    participant H as live handler
    participant S as live.Service
    participant D as seller_directory
    participant R as mongodb adapter

    C->>MW: POST /api/v1/live/streams พร้อม bearer token
    MW->>H: subject อยู่บน context
    H->>H: ตรวจความถูกต้องของ title
    H->>S: Create(ctx, title, userID)
    S->>D: ByUserID(ctx, subject)
    alt ไม่มีร้าน หรือ event ของ seller ยังมาไม่ถึง
        D-->>S: ไม่พบ
        H-->>C: 409 unknown seller
    else รู้จักแล้ว
        D-->>S: SellerRef
        S->>R: Create(ctx, Stream)
        R-->>S: Stream สถานะ scheduled
        H-->>C: 201 stream
    end
```

## เริ่มและจบ

```mermaid
sequenceDiagram
    autonumber
    actor C as เจ้าของไลฟ์
    participant H as live handler
    participant S as live.Service
    participant R as mongodb adapter
    participant B as redisbus

    C->>H: POST /api/v1/live/streams/{id}/start หรือ /end
    H->>S: Start หรือ End(ctx, id, userID)
    S->>R: ByID(ctx, id)
    alt ไม่ใช่เจ้าของไลฟ์
        S-->>H: ErrNotHost
        H-->>C: 403 forbidden
    else เป็นเจ้าของ
        alt เปลี่ยนสถานะแบบนี้จากสถานะปัจจุบันไม่ได้
            S-->>H: ErrInvalidTransition
            H-->>C: 409 conflict
        else เปลี่ยนได้
            S->>R: อัปเดตสถานะและบันทึก started_at หรือ ended_at
            S->>B: publish stream.started หรือ stream.ended
            Note over S,B: ใช้ Redis pub/sub ผู้ชมบนทุก instance จึงได้รับ<br/>ไม่ใช่แค่คนที่ต่ออยู่กับ process นี้
            H-->>C: 200 stream
        end
    end
```

## ⭐⭐ เข้าดู — WebSocket

รูปที่แน่นที่สุดในไฟล์นี้ และสามรายละเอียดของมันเป็นแบบที่จะเห็นก็ต่อเมื่อมี client จริงมาต่อ

**ต้องอ่านจาก socket แม้ client จะไม่ส่งอะไรมาเลย** เพราะการปิด WebSocket
จะรู้ได้จากการอ่านเท่านั้น handler ที่ไม่เคยอ่านจะไม่มีวันรู้ว่าผู้ชมออกไปแล้ว
และรายการ presence จะค้างอยู่จนกว่าจะหมดอายุ

**ส่ง snapshot ไม่ใช่ event การเข้าร่วม** เวอร์ชันแรกส่ง `viewer.joined`
ตรงไปยัง socket ใหม่ *และ* ปล่อยให้มันมาถึงอีกรอบจาก broadcast ที่เพิ่ง subscribe ไป
ผู้ชมทุกคนจึงเห็นข้อความซ้ำสองครั้ง ตอนนี้เปลี่ยนเป็น `stream.state` snapshot
ซึ่งมีประโยชน์กว่า และไม่ซ้ำ

**ถ้าผู้ชมคนไหนช้า ให้ทิ้ง frame แทนที่จะ block** การเชื่อมต่อที่ค้างอยู่หนึ่งเส้น
ต้องไม่ฉุดทุกคนที่ใช้ process เดียวกัน

```mermaid
sequenceDiagram
    autonumber
    actor V as ผู้ชม
    participant H as live handler
    participant S as live.Service
    participant Rd as Redis
    participant B as redisbus subscription

    V->>H: GET /api/v1/live/streams/{id}/watch พร้อม Upgrade
    alt ไม่มีไลฟ์นี้
        H-->>V: 404 ก่อนจะ upgrade
    else มีอยู่
        H->>H: รับการ upgrade เป็น WebSocket
        H->>Rd: ZADD presence:{id} score=now member=viewerID
        H->>B: subscribe ไปที่ stream:{id}
        H->>S: สถานะปัจจุบันและจำนวนผู้ชม
        H-->>V: stream.state snapshot — สถานะ สินค้าที่ปักไว้ จำนวนผู้ชม
        Note over H,V: เป็น snapshot ไม่ใช่ viewer.joined<br/>การเข้าร่วมจะมาถึงครั้งเดียว จาก broadcast

        par สามอย่างทำงานพร้อมกันตลอดอายุของ socket
            loop จนกว่า socket จะปิด
                B-->>H: frame ที่ broadcast มาจาก instance ไหนก็ได้
                alt buffer ส่งของผู้ชมคนนี้เต็ม
                    H->>H: ทิ้ง frame แล้วนับไว้
                    Note over H: ผู้ชมที่ช้าต้องไม่ทำให้<br/>คนอื่นใน process นี้ค้างตาม
                else ยังส่งได้
                    H-->>V: frame
                end
            end
        and
            loop ทุกช่วง heartbeat
                H->>Rd: ZADD presence:{id} รีเฟรช score ของผู้ชมคนนี้
                Note over H,Rd: ไม่มีใครส่งคำว่าลาก่อนตอนปิดฝาโน้ตบุ๊ก<br/>presence จึงต้องหมดอายุเอง<br/>ไม่ใช่รอให้ลบตอนออก
            end
        and
            loop จนกว่า socket จะปิด
                H->>V: อ่าน
                Note over H,V: ต้องอ่านแม้ client จะไม่ส่งอะไรมา<br/>เพราะการปิดจะเห็นได้จากการอ่านเท่านั้น
            end
        end

        V--xH: การเชื่อมต่อปิดลง
        H->>B: unsubscribe
        H->>Rd: ZREM presence:{id} ผู้ชมคนนี้
    end
```

## ⭐ ปักสินค้า — กระจายข้ามอินสแตนซ์

นี่คือรูปที่แสดงว่าทำไม Redis ถึงเป็นของบังคับ ไม่ใช่ของเสริม
เจ้าของไลฟ์ต่ออยู่กับ instance เดียว ส่วนผู้ชมกระจายอยู่ทุก instance

**ใช้ Redis pub/sub ไม่ใช่ Kafka** และตั้งใจแบบนั้น เพราะมันไม่มี replay
ซึ่งถูกต้องสำหรับ feed ที่มูลค่าหมดไปในไม่กี่วินาที และผิดสำหรับอะไรที่ต้องคงทน
นี่ยังเป็น publisher เดียวในระบบที่ **ไม่มี outbox** ด้วยเหตุผลเดียวกัน —
การรับประกันการส่งมอบ ของการแจ้งเตือนที่ไร้ค่าเมื่อมันเก่า คือกลไกที่สร้างมาเพื่อความว่างเปล่า

```mermaid
sequenceDiagram
    autonumber
    actor C as เจ้าของไลฟ์
    participant H1 as live instance ที่ 1
    participant S as live.Service
    participant R as mongodb adapter
    participant Rd as Redis pub/sub
    participant H2 as live instance ที่ 2
    actor V1 as ผู้ชมบน instance 1
    actor V2 as ผู้ชมบน instance 2

    C->>H1: POST /api/v1/live/streams/{id}/feature
    H1->>S: Feature(ctx, id, productID, userID)
    alt ไม่ใช่เจ้าของไลฟ์
        H1-->>C: 403 forbidden
    else เป็นเจ้าของ
        S->>R: ตั้งค่า featured_product_id
        R-->>S: Stream
        S->>Rd: PUBLISH stream:{id} product.featured
        H1-->>C: 200 stream
    end

    par Redis กระจายข้อความไปยัง subscriber ทุกตัว
        Rd-->>H1: product.featured
        H1-->>V1: product.featured
    and
        Rd-->>H2: product.featured
        H2-->>V2: product.featured
    end
    Note over V1,V2: ผู้ชมทั้งสองเห็นเหมือนกัน ถ้าไม่มี Redis<br/>จะมีแค่คนบน instance 1 ที่เห็น<br/>และ instance 2 จะไม่รู้เลยว่าเกิดอะไรขึ้น
```

## ⭐ presence ที่ต้องหมดอายุได้

จำนวนผู้ชมเก็บใน sorted set ที่ให้คะแนนด้วย timestamp และตัดทิ้งตอนอ่าน
เหตุผลตรงไปตรงมา: **ไม่มีใครส่งคำว่าลาก่อนตอนปิดฝาโน้ตบุ๊ก**
ดีไซน์ที่ลบตอน disconnect จะนับผู้ชมคนนั้นไปตลอดกาลตั้งแต่ครั้งแรกที่การเชื่อมต่อตายแบบไม่บอกกล่าว
และการเชื่อมต่อตายแบบนั้นตลอดเวลา

```mermaid
sequenceDiagram
    autonumber
    participant H as live handler
    participant Rd as Redis sorted set
    actor V as ผู้ชม

    rect rgb(240, 255, 240)
        Note over V,Rd: ระหว่างที่ socket ยังเปิดอยู่
        V->>H: เชื่อมต่อแล้ว
        H->>Rd: ZADD presence:{id} now viewerID
        loop ทุก heartbeat
            H->>Rd: ZADD รีเฟรช score เป็นเวลาปัจจุบัน
        end
    end

    rect rgb(255, 245, 238)
        Note over V,Rd: การหลุดแบบไม่บอกกล่าว — ไม่มี close frame มาถึงเลย
        V--xH: ปิดฝาโน้ตบุ๊ก เน็ตหลุด
        Note over H: การอ่านของ handler จะ error ในที่สุด<br/>แต่อาจใช้เวลาสักพัก
    end

    rect rgb(240, 248, 255)
        Note over H,Rd: ทุกครั้งที่มีการอ่านจำนวนผู้ชม
        H->>Rd: ZREMRANGEBYSCORE presence:{id} 0 (now - ttl)
        Note over H,Rd: ตัดทิ้งก่อน ใครที่ไม่ได้ heartbeat มาสักพัก<br/>ถือว่าไปแล้ว ไม่ว่าจะบอกลาหรือไม่
        H->>Rd: ZCARD presence:{id}
        Rd-->>H: จำนวนผู้ชมที่ยังอยู่จริง
    end
```

## รายการและรายตัว

```mermaid
sequenceDiagram
    autonumber
    actor C as ผู้ใช้
    participant H as live handler
    participant S as live.Service
    participant R as mongodb adapter
    participant Rd as Redis

    C->>H: GET /api/v1/live/streams หรือ /api/v1/live/streams/{id}
    Note over C,H: เป็น public — ไม่ต้องมี token<br/>ก็ดูได้ว่ามีไลฟ์อะไรอยู่บ้าง
    alt ขอเป็นรายการ
        H->>S: List(ctx, limit, offset)
        S->>R: find พร้อมนับจำนวน
        H-->>C: 200 streams, total, limit, offset
    else ขอรายตัว
        H->>S: ByID(ctx, id)
        S->>R: ByID
        alt ไม่มีของ
            H-->>C: 404 stream not found
        else เจอ
            S->>Rd: ตัดทิ้งก่อน แล้ว ZCARD presence:{id}
            Rd-->>S: จำนวนผู้ชมปัจจุบัน
            H-->>C: 200 stream พร้อมจำนวนผู้ชมสด
        end
    end
```
