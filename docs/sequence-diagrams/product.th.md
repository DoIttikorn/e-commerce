# Product — UML Sequence Diagram

*English: [product.md](product.md) · สารบัญ: [README.th.md](README.th.md)*

แคตตาล็อกสินค้า และเป็น domain ที่มีเนื้อเทคนิคแน่นที่สุดในหกตัว มันทำ cache ผ่าน Redis
สร้าง read model จาก Kafka และให้บริการจอง stock ผ่าน gRPC ที่ป้องกันด้วย mutual TLS

| Flow | Endpoint |
|---|---|
| ⭐ [อ่านสินค้ารายชิ้น](#-อ่านสินค้ารายชิ้น--cache-aside) | `GET /api/v1/products/{id}` |
| [รายการสินค้า](#รายการสินค้า--ตั้งใจไม่-cache) | `GET /api/v1/products` |
| [สร้างสินค้า](#สร้างสินค้า) | `POST /api/v1/products` |
| ⭐ [แก้ไข](#-แก้ไข--ล้าง-cache-ไม่ใช่เขียนทับ) | `PATCH /api/v1/products/{id}` |
| [ลบ](#ลบ) | `DELETE /api/v1/products/{id}` |
| ⭐ [consume event จาก seller](#-consume-stream-ของ-seller) | Kafka `seller.events` |
| ⭐⭐ [จอง stock](#-จอง-stock-grpc--mutual-tls) | gRPC `StockService/Reserve` |
| ⭐ [Confirm และ reaper](#-confirm-และ-reaper) | gRPC `StockService/Confirm` |

---

## ⭐ อ่านสินค้ารายชิ้น — cache-aside

cache เป็น **decorator ที่ implement `Repository` port** ไม่ใช่ if-else ใน service
ตัว service เขียนเหมือนไม่มี cache อยู่เลย และถ้าจะเอา cache ออกก็แก้บรรทัดเดียวใน `main`

สังเกตกิ่งที่ fail: เมื่อ Redis ใช้ไม่ได้ request จะ **ตกไปอ่าน MongoDB** แทนที่จะพัง
เพราะ cache ที่ทำให้ request พังตอนมันล่ม ไม่ใช่ cache แล้ว แต่กลายเป็น dependency
มันทำให้ availability แย่ลง ซึ่งตรงข้ามกับเหตุผลที่ใส่มันเข้ามาตั้งแต่แรก

```mermaid
sequenceDiagram
    autonumber
    actor C as ผู้ใช้
    participant H as handler
    participant S as product.Service
    participant Ch as rediscache decorator
    participant Rd as Redis
    participant R as mongodb adapter
    participant DB as MongoDB

    C->>H: GET /api/v1/products/{id}
    H->>S: ByID(ctx, id)
    S->>Ch: ByID(ctx, id)
    Ch->>Rd: GET product:{id}
    alt เจอใน cache
        Rd-->>Ch: JSON ที่ cache ไว้
        Ch->>Ch: เพิ่มตัวนับ hit
        Ch-->>S: Product
    else ไม่เจอ หรือ Redis ล่ม
        Rd-->>Ch: nil หรือ connection error
        Ch->>Ch: เพิ่มตัวนับ miss
        Note over Ch,Rd: error จาก Redis จะตกลงไปอ่าน DB ต่อ<br/>มันเป็นสัญญาณ readiness ไม่ใช่ liveness
        Ch->>R: ByID(ctx, id)
        R->>DB: findOne ด้วย _id
        alt ไม่มีของ
            DB-->>R: ErrNoDocuments
            R-->>Ch: ErrProductNotFound
            Ch-->>S: ErrProductNotFound
            H-->>C: 404 product not found
        else เจอ
            DB-->>R: document
            R-->>Ch: Product
            Ch->>Rd: SET product:{id} พร้อม TTL แบบ best effort
            Ch-->>S: Product
        end
    end
    S-->>H: Product
    H-->>C: 200 product
```

## รายการสินค้า — ตั้งใจไม่ cache

รายการที่แบ่งหน้า **ไม่** ถูก cache และเป็นการตัดสินใจ ไม่ใช่การลืม
เพราะทุกชุด filter และทุกหน้าคือ key คนละอัน การเขียนครั้งเดียวจะต้องล้าง key
จำนวนที่ไม่รู้ล่วงหน้า

```mermaid
sequenceDiagram
    autonumber
    actor C as ผู้ใช้
    participant H as handler
    participant S as product.Service
    participant Ch as rediscache decorator
    participant R as mongodb adapter
    participant DB as MongoDB

    C->>H: GET /api/v1/products?seller_id=...&limit=20
    Note over C,H: เป็น public — แคตตาล็อกที่บังคับล็อกอิน<br/>คือแคตตาล็อกที่ไม่มีใครเข้าดู
    H->>S: List(ctx, filter, limit, offset)
    S->>Ch: List(...)
    Ch->>R: List(...) — ส่งผ่านตรง ๆ ไม่มีการ cache
    R->>DB: find โดยใช้ index seller_id + created_at
    Note over R,DB: เป็น compound index ที่เรียงตรงกับ sort<br/>MongoDB จึงเดินตาม index ได้<br/>แทนที่จะ sort ใน memory
    DB-->>R: หน้าข้อมูลและจำนวนรวม
    R-->>Ch: products และ total
    Ch-->>S: products และ total
    S-->>H: products และ total
    H-->>C: 200 products, total, limit, offset
```

## สร้างสินค้า

ร้านค้าต้องเป็นที่รู้จัก **ในเครื่องนี้** อยู่แล้ว Product ไม่ได้เรียกไปถาม Seller
แต่ดูจาก read model ที่สร้างจาก event stream ของ seller
ถ้า event ยังมาไม่ถึง คำตอบคือ **409 ไม่ใช่ 404** เพราะบัญชีนั้นอาจมีร้านอยู่จริง
และการลองใหม่คือสิ่งที่ควรทำ ซึ่ง conflict สื่อความนั้น ส่วน not-found ไม่สื่อ

```mermaid
sequenceDiagram
    autonumber
    actor C as ผู้ใช้
    participant MW as auth middleware
    participant H as handler
    participant S as product.Service
    participant D as seller_directory
    participant R as mongodb adapter
    participant DB as MongoDB

    C->>MW: POST /api/v1/products พร้อม bearer token
    MW->>H: subject อยู่บน context
    H->>H: ตรวจ name, price_minor, currency, stock
    alt ไม่ผ่าน
        H-->>C: 400 validation failed พร้อม fields
    else ผ่าน
        H->>S: Create(ctx, NewProduct, userID)
        S->>D: ByUserID(ctx, subject)
        alt event ของ seller ยังมาไม่ถึง
            D-->>S: ไม่พบ
            S-->>H: ErrUnknownSeller
            H-->>C: 409 unknown seller — ลองใหม่ได้
        else รู้จักแล้ว
            D-->>S: SellerRef ที่มี id และ name
            Note over S,D: SellerRef เป็น type เล็ก ๆ ของ Product เอง<br/>ถ้าไปใช้ seller.Seller ร่วมกัน จะผูกสองตัวติดกัน<br/>จน deploy แยกกันไม่ได้
            S->>S: denormalise seller_name ลงบนสินค้า
            S->>S: ประกอบ event product.created
            S->>R: Create(ctx, Product, events)
            R->>DB: transaction — insert product และ insert outbox
            DB-->>R: commit
            R-->>S: Product
            H-->>C: 201 product
        end
    end
```

## ⭐ แก้ไข — ล้าง cache ไม่ใช่เขียนทับ

ตรงนี้เห็นกฎสองข้อ

**การเขียนจะล้าง cache ไม่ใช่เขียนทับ** เพราะถ้าเขียนทับ เมื่อมีคนเขียนพร้อมกันสองคน
ค่าเก่ากว่าอาจค้างใน cache ตลอดไป — ใครเขียนลง Redis ทีหลังคนนั้นชนะ
ซึ่งไม่จำเป็นต้องเป็นคนเดียวกับที่เขียนลง MongoDB ทีหลัง

**การล้างทำแบบเจาะจง ไม่ใช่การ scan** repository คืน ID ที่ได้รับผลกระทบมาให้
decorator ลบเฉพาะ key เหล่านั้น การ scan Redis หา key ที่อาจตรงคือ `O(keyspace)`
และจะยิ่งช้าลงพอดีตอนที่ระบบยิ่งมีงานเยอะ

```mermaid
sequenceDiagram
    autonumber
    actor C as ผู้ใช้
    participant H as handler
    participant S as product.Service
    participant Ch as rediscache decorator
    participant R as mongodb adapter
    participant DB as MongoDB
    participant Rd as Redis

    C->>H: PATCH /api/v1/products/{id}
    H->>S: Update(ctx, id, Update, userID)
    S->>Ch: ByID เพื่อตรวจความเป็นเจ้าของ
    Ch-->>S: Product
    alt สินค้าเป็นของร้านอื่น
        S-->>H: ErrNotOwner
        H-->>C: 403 this product belongs to another shop
    else เป็นเจ้าของ
        S->>Ch: Update(ctx, id, Update, events)
        Ch->>R: Update(ctx, id, Update, events)
        R->>DB: transaction — update product และ insert outbox
        DB-->>R: commit
        R-->>Ch: Product และ id ที่ได้รับผลกระทบ
        Ch->>Rd: DEL product:{id} — ลบ ไม่ใช่ SET
        Note over Ch,Rd: เฉพาะ key ที่ได้รับผลกระทบเท่านั้น<br/>ไม่ SCAN ไม่ใช้ pattern
        Ch-->>S: Product
        H-->>C: 200 product
    end
```

## ลบ

```mermaid
sequenceDiagram
    autonumber
    actor C as ผู้ใช้
    participant H as handler
    participant S as product.Service
    participant Ch as rediscache decorator
    participant R as mongodb adapter
    participant Rd as Redis

    C->>H: DELETE /api/v1/products/{id}
    H->>S: Delete(ctx, id, userID)
    S->>Ch: ByID เพื่อตรวจความเป็นเจ้าของ
    alt ไม่ใช่เจ้าของ
        S-->>H: ErrNotOwner
        H-->>C: 403 forbidden
    else เป็นเจ้าของ
        S->>Ch: Delete(ctx, id, events)
        Ch->>R: ลบสินค้าพร้อมเขียน outbox ใน transaction เดียว
        R-->>Ch: สำเร็จ
        Ch->>Rd: DEL product:{id}
        Ch-->>S: สำเร็จ
        H-->>C: 204 no content
    end
```

## ⭐ consume stream ของ seller

กฎสามข้อที่มาจากสิ่งที่เคยพังมาแล้วจริง ๆ

**commit หลังจากจัดการเสร็จ ห้ามก่อน** ใช้ `FetchMessage` + `CommitMessages`
แทน `ReadMessage` เพื่อให้การ crash กลาง handler ทำให้ข้อความถูกส่งซ้ำ ไม่ใช่หายไป

**handler ต้อง idempotent** เพราะการส่งเป็นแบบ at-least-once และ commit เกิดหลัง
handler สำเร็จ การได้รับซ้ำจึงเป็นเรื่องที่ต้องเกิดแน่ ๆ ไม่ใช่แค่อาจเกิด

**ข้อความที่ถอดรหัสไม่ได้ถาวร ให้คืน nil** ไม่ใช่ error เพราะการ retry มันตลอดไป
จะบล็อก partition นั้นสำหรับทุกข้อความที่ต่อคิวอยู่ข้างหลัง

```mermaid
sequenceDiagram
    autonumber
    participant K as Kafka seller.events
    participant Cn as kafka.Consumer
    participant Ev as product events handler
    participant S as product.Service
    participant D as seller_directory
    participant Ch as rediscache decorator
    participant Rd as Redis

    loop จนกว่า context จะถูกยกเลิก
        Cn->>K: FetchMessage
        K-->>Cn: message พร้อม header และ payload
        Cn->>Cn: ดึง trace context ออกจาก header
        Cn->>Ev: handle(ctx, key, value)
        alt payload ถอดรหัสไม่ได้ถาวร
            Ev-->>Cn: nil — log ไว้แล้วไปต่อ
            Note over Ev,Cn: ถ้าคืน error ตรงนี้ จะ retry ตลอดไป<br/>และบล็อก partition<br/>ระบบจริงจะส่งไป dead-letter topic
        else seller.registered
            Ev->>S: UpsertSeller(ctx, SellerRef)
            S->>D: upsert ด้วย seller id — idempotent โดยธรรมชาติ
        else seller.updated
            Ev->>S: RenameSeller(ctx, id, name)
            S->>D: upsert รายการใน directory
            S->>Ch: เขียน seller_name ใหม่บนสินค้าของร้านนั้น
            Ch->>Rd: DEL เฉพาะ key ของสินค้าที่ได้รับผลกระทบ
            Ch-->>S: id ที่ได้รับผลกระทบ
        end
        Ev-->>Cn: nil
        Cn->>K: CommitMessages — ตอนนี้เท่านั้น
    end
```

## ⭐⭐ จอง stock (gRPC + mutual TLS)

เป็น synchronous call เดียวระหว่าง service ในระบบทั้งหมด และเป็นรูปที่แน่นที่สุดใน folder นี้
มีสี่แนวคิดที่เป็นแกนหลัก

1. **ใช้ mutual TLS ไม่ใช่ bearer token** เพราะ call นี้ไม่มี user อยู่เลย
   คำถามคือ *service ไหนเป็นคนถาม* ซึ่งมีแต่ client certificate ที่ตอบได้
2. **การเช็ค idempotency เกิดขึ้นนอก transaction** ถ้าดัก duplicate-key error
   ไว้ข้างในแล้วคืน success เท่ากับสั่งให้ driver commit transaction ที่มี write
   ที่ fail อยู่ข้างในแล้ว commit จะ fail แบบ retryable แล้ว `WithTransaction`
   จะ retry ตัวซ้ำก็ยังอยู่ วนจน context หมดอายุ bug นี้เคยทำ test ค้าง 90 วินาที
3. **การหักสต็อกเป็น conditional update ไม่ใช่ lock** — `{_id, stock: {$gte: n}}`
   คู่กับ `$inc` server จับคู่และหักในขั้นตอนเดียว ผู้ซื้อสองคนที่แย่งชิ้นสุดท้าย
   จึงสำเร็จพร้อมกันไม่ได้
4. **transaction คือสิ่งที่ทำให้ได้ทั้งหมดหรือไม่ได้เลยข้ามหลายรายการ** เพราะต่อรายการนั้น
   conditional update atomic อยู่แล้ว transaction มีไว้เพื่อไม่ให้การพังที่รายการที่สาม
   ทิ้งรายการที่หนึ่งกับสองที่หักไปแล้วค้างไว้

```mermaid
sequenceDiagram
    autonumber
    participant O as Order service
    participant T as ชั้น TLS
    participant G as product gapi
    participant S as product.Service
    participant R as mongodb stock adapter
    participant DB as MongoDB

    O->>T: Reserve(items, idempotency_key)
    T->>T: ยื่น client certificate และตรวจของฝั่ง server
    alt ไม่มี client certificate หรือเซ็นโดย CA อื่น
        T-->>O: handshake ล้มเหลว — RPC ไปไม่ถึง server เลย
        Note over O,T: RequireAndVerifyClientCert<br/>ถ้าไม่มีข้อนี้ อะไรก็ตามที่ส่ง packet มาถึงได้<br/>ก็จองสินค้าได้
    else certificate ผ่านทั้งสองฝั่ง
        T->>G: Reserve
        G->>S: Reserve(ctx, key, items)
        S->>R: Reserve(ctx, key, items)

        R->>DB: findOne ที่ stock_reservations ด้วย _id = key
        alt key นี้เคยถูกใช้แล้ว
            DB-->>R: การจองครั้งก่อน
            R-->>S: รายการเดิมเป๊ะกับครั้งที่แล้ว
            Note over R,DB: อยู่นอก transaction โดยตั้งใจ<br/>ดูข้อ 2 ด้านบน
        else key ใหม่
            R->>DB: เริ่ม transaction
            R->>DB: insertOne ที่ stock_reservations โดย _id = key
            loop ทุกรายการสินค้า
                R->>DB: findOneAndUpdate {_id, stock: $gte n} ด้วย $inc -n
                alt ไม่มี document ตรงเงื่อนไข
                    R->>DB: findOne เพื่อแยกสองกรณีออกจากกัน
                    alt ไม่มีสินค้านี้
                        R-->>S: ErrProductNotFound
                    else มีสินค้าแต่ stock ไม่พอ
                        R-->>S: ErrInsufficientStock
                    end
                    DB-->>R: abort — ไม่มีอะไรถูกหัก
                end
            end
            R->>DB: บันทึกรายการที่จองไว้ลงในเอกสารการจอง
            DB-->>R: commit
        end
        R-->>S: รายการที่จองได้
        S-->>G: รายการที่จองได้
        alt ErrInsufficientStock
            G-->>O: FailedPrecondition
        else ErrProductNotFound
            G-->>O: NotFound
        else สำเร็จ
            G-->>O: ReserveResponse
        end
    end
```

## ⭐ Confirm และ reaper

การจองแบบสองเฟส `Reserve` หักสต็อกไว้ ส่วน `Confirm` บอกว่ามี order ถูกเขียนจริงแล้ว
อะไรก็ตามที่ยังไม่ confirm หลังพ้นช่วงผ่อนผัน แปลว่าเป็นของ caller ที่ตายไประหว่างสองขั้นตอน
และตรรกะการชดเชยใน caller ก็ช่วยไม่ได้ เพราะ caller คือสิ่งที่ตายไป

ช่วงผ่อนผันตั้งไว้เผื่อเยอะโดยตั้งใจ เพราะการไปยึดสต็อกคืนจาก order ที่สั่งจริง
เป็นความล้มเหลวที่แย่กว่าการถือสต็อกไว้นานอีกหน่อยมาก

```mermaid
sequenceDiagram
    autonumber
    participant O as Order service
    participant G as product gapi
    participant S as product.Service
    participant R as mongodb stock adapter
    participant DB as MongoDB
    participant Rp as reaper ทุกหนึ่งนาที

    rect rgb(240, 248, 255)
        Note over O,DB: เฟสที่สอง บนเส้นทางปกติ
        O->>G: Confirm(idempotency_key)
        G->>S: Confirm(ctx, key)
        S->>R: Confirm(ctx, key)
        R->>DB: updateByID ตั้ง confirmed = true โดยไม่ upsert
        Note over R,DB: ไม่ upsert — การ confirm key ที่ไม่เคยจอง<br/>ควรไม่ทำอะไรเลย<br/>ไม่ใช่ไปสร้างการจองขึ้นมาใหม่
        DB-->>R: สำเร็จ
        G-->>O: response เปล่า
    end

    rect rgb(255, 245, 238)
        Note over Rp,DB: ตาข่ายนิรภัย เมื่อ caller ตายไป
        loop ทุกหนึ่งนาที
            Rp->>R: ReleaseExpired(ctx, 15 นาที)
            R->>DB: find confirmed=false, released=false, created_at < cutoff
            DB-->>R: การจองค้างไม่เกิน 500 รายการ
            loop ทีละรายการ
                R->>DB: findOneAndUpdate {_id, released:false} ตั้ง released=true
                Note over R,DB: จองสิทธิ์และตั้งธงในขั้นตอนเดียว<br/>การปล่อยพร้อมกันสองครั้ง<br/>จึงคืนสต็อกซ้ำไม่ได้
                alt ปล่อยไปแล้วหรือไม่รู้จัก
                    DB-->>R: ไม่มี document — ไม่เป็นไร ข้าม
                else จองสิทธิ์ได้
                    loop ทุกรายการที่จองไว้
                        R->>DB: updateByID $inc stock +quantity
                    end
                end
            end
            R-->>Rp: จำนวนที่ปล่อยคืน
        end
    end
```
