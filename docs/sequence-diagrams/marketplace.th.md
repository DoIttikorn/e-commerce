# Marketplace — UML Sequence Diagram

*English: [marketplace.md](marketplace.md) · สารบัญ: [README.th.md](README.th.md)*

projection สำหรับค้นหา ที่ถูกป้อนด้วย event **สามสาย** service นี้ไม่มีการเขียน
เป็นของตัวเองเลย ทุก row เกิดจาก event ที่คนอื่น publish ซึ่งเป็นเหตุผลว่าทำไม
มันมี endpoint เดียวแต่มี consumer สามตัว

| Flow | Endpoint |
|---|---|
| ⭐ [ค้นหา](#-ค้นหา--projection-บวก-ttl-cache) | `GET /api/v1/marketplace/listings` |
| ⭐ [สาม stream หนึ่ง projection](#-สาม-stream-หนึ่ง-projection) | Kafka |
| [การสร้าง text index](#การสร้าง-text-index) | ตอน startup |

---

## ⭐ ค้นหา — projection บวก TTL cache

มีสองอย่างที่ควรสังเกต

**projection คือเหตุผลที่อันนี้เป็น query เดียว** ถ้าจะตอบว่า "แก้วราคาต่ำกว่า ฿500
จากร้านที่ยัง active เรียงตามยอดขาย" จากข้อมูลต้นทาง จะต้อง join สาม service
แต่ตรงนี้เป็น find ที่มี index รองรับครั้งเดียว เพราะการ join เกิดไปแล้วตอน event มาถึง

**cache เป็น TTL ไม่ใช่กลไกล้างข้อมูล** search cache คือเพดานของความเก่า
ไม่ใช่กลไกความถูกต้อง เพราะไม่มีใครเดือดร้อนถ้าสินค้าโผล่ช้าไปไม่กี่วินาที
และทางเลือกอีกทาง คือการล้างทุก query ที่อาจตรงกับสินค้าที่เปลี่ยน ซึ่งคำนวณไม่ได้

```mermaid
sequenceDiagram
    autonumber
    actor C as ผู้ใช้
    participant H as marketplace handler
    participant S as marketplace.Service
    participant Ch as rediscache decorator
    participant Rd as Redis
    participant R as mongodb adapter
    participant DB as MongoDB ของ marketplace

    C->>H: GET /api/v1/marketplace/listings?q=mug&sort=best_selling
    Note over C,H: เป็น public ไม่ต้องใช้ token
    H->>H: แปลงและจำกัดค่า q, ช่วงราคา, in_stock, sort, การแบ่งหน้า
    alt sort ไม่ใช่ค่าที่รู้จัก
        H-->>C: 400 validation failed พร้อม fields
    else ผ่าน
        H->>S: Search(ctx, Query)
        S->>Ch: Search(ctx, Query)
        Ch->>Ch: สร้าง cache key จาก query ทั้งชุดที่ normalise แล้ว
        Ch->>Rd: GET search:{hash}
        alt เจอใน cache
            Rd-->>Ch: หน้าผลลัพธ์ที่ cache ไว้
            Ch-->>S: listings และ total
        else ไม่เจอ หรือ Redis ใช้ไม่ได้
            Note over Ch,Rd: ตกลงไปอ่าน MongoDB ต่อ<br/>Redis ตรงนี้เป็นสัญญาณ readiness<br/>ไม่ใช่ liveness
            Ch->>R: Search(ctx, Query)
            alt มีคำค้น q
                R->>DB: find ด้วย $text พร้อม filter เรียงตาม textScore
                Note over R,DB: เป็น text index จริง ตัดรากคำและจัดอันดับได้<br/>ไม่ใช่การ match substring
            else ไม่มี q
                R->>DB: find ด้วย filter เรียงตาม field ที่ขอมา
            end
            DB-->>R: listings และ total
            R-->>Ch: listings และ total
            Ch->>Rd: SET search:{hash} ด้วย TTL สั้น ๆ
            Ch-->>S: listings และ total
        end
        S-->>H: listings และ total
        H-->>C: 200 listings, total, limit, offset, sort
    end
```

## ⭐ สาม stream หนึ่ง projection

โค้ดใน Seller, Product และ Order ไม่รู้จัก Marketplace เลยสักตัว
พวกมันแค่ประกาศว่ามีอะไรเกิดขึ้น ใครสนใจก็มา subscribe เอง
นี่คือความต่างระหว่าง event กับการเรียกตรง ๆ: ถ้าจะเพิ่ม consumer ตัวที่เจ็ด
ไม่ต้องแก้ publisher เลยสักตัว

ทุก handler เป็น idempotent เพราะการส่งแบบ at-least-once ทำให้การได้รับซ้ำ
เป็นเรื่องที่ต้องเกิดแน่ ไม่ใช่แค่อาจเกิด

```mermaid
sequenceDiagram
    autonumber
    participant KS as Kafka seller.events
    participant KP as Kafka product.events
    participant KO as Kafka order.events
    participant Cn as consumer สามตัว คนละ group
    participant S as marketplace.Service
    participant DB as MongoDB ของ marketplace

    par สาม stream ทำงานเป็นอิสระต่อกัน
        KS->>Cn: seller.registered หรือ seller.updated
        Cn->>S: UpsertSeller / RenameSeller
        S->>DB: upsert seller_name บนรายการของร้านนั้นทั้งหมด
        Note over S,DB: การเปลี่ยนชื่อร้านไปถึง service ตัวที่สาม<br/>ที่ไม่เคยเรียก service ตัวที่สองเลย
    and
        KP->>Cn: product.created, product.updated, product.deleted
        Cn->>S: UpsertListing หรือ RemoveListing
        S->>DB: upsert หรือ delete ด้วย product_id
        Note over S,DB: ใช้ product id เป็น key การเขียนจึง idempotent<br/>ไม่ว่า event เดิมจะมาถึงกี่ครั้ง
    and
        KO->>Cn: order.paid
        Cn->>S: RecordSale(ctx, lines)
        loop ทุกรายการในออร์เดอร์
            S->>DB: $inc sold_count ตามจำนวนที่ซื้อ
        end
        Note over S,DB: นี่คือสิ่งที่ทำให้ sort=best_selling เป็นไปได้<br/>โดยไม่ต้องไปถาม Order เลย
    end
    Cn->>Cn: commit offset ของแต่ละตัว หลัง handler สำเร็จเท่านั้น
```

## การสร้าง text index

การสร้าง index เป็นหน้าที่ของ adapter ประจำ domain ไม่ใช่ของ package database ที่ใช้ร่วมกัน
การเพิ่ม domain ใหม่จึงไม่เคยต้องไปแก้ `internal/database`

```mermaid
sequenceDiagram
    autonumber
    participant M as cmd/marketplace main
    participant A as appserver
    participant R as mongodb adapter
    participant DB as MongoDB ของ marketplace
    participant K as Kafka

    M->>A: New(ctx, "marketplace")
    A->>DB: เชื่อมต่อและ ping
    M->>R: EnsureIndexes(ctx)
    R->>DB: สร้าง text index ครอบ name และ seller_name
    R->>DB: สร้าง index สำหรับ filter ราคาและ sold_count
    R->>DB: สร้าง index ของ outbox
    M->>K: EnsureTopic สำหรับทุก topic ที่มัน consume
    Note over M,K: สร้างตอน startup แทนที่จะพึ่ง auto-creation<br/>เพราะ consumer ที่ subscribe ก่อนมีการ publish ครั้งแรก<br/>จะแข่งกับการสร้าง topic แล้วนั่งเงียบไปเฉย ๆ
    M->>A: เริ่ม consumer สามตัวเป็น background task
    M->>A: Run — ให้บริการจนกว่าจะมีสัญญาณให้หยุด
```
