# UML Sequence Diagram

*English: [README.md](README.md)*

**42 diagram ต่อภาษา รวม 84 รูป และทุกรูป parse ผ่าน** — ตรวจด้วย parser ของ mermaid เอง ไม่ใช่การกวาดตาดู

ทุก endpoint ในระบบ วาดออกมาให้เห็น อ่าน folder นี้เมื่ออยากรู้ว่าเกิดอะไรขึ้นจริง ๆ
ระหว่างที่ request เข้ามาจนถึงตอน response ออกไป — รวมถึงส่วนที่เกิดขึ้น
**หลังจาก** response ออกไปแล้วด้วย

## จัดวางอย่างไร

แต่ละ domain มีไฟล์ละภาษา ตามธรรมเนียมที่ repo นี้ใช้อยู่แล้ว (`x.md` / `x.th.md`)
ไฟล์ภาษาไทยคือคำแปลของ diagram ชุดเดียวกัน ไม่ใช่คนละชุด — diagram ที่แก้
ต้องแก้ทั้งสองภาษา

| | Diagram | EN | TH |
|---|---|---|---|
| **User** | 9 | [user.md](user.md) | [user.th.md](user.th.md) |
| **Seller** | 4 | [seller.md](seller.md) | [seller.th.md](seller.th.md) |
| ⭐ **Product** | 8 | [product.md](product.md) | [product.th.md](product.th.md) |
| ⭐⭐ **Order** | 5 | [order.md](order.md) | [order.th.md](order.th.md) |
| **Marketplace** | 3 | [marketplace.md](marketplace.md) | [marketplace.th.md](marketplace.th.md) |
| ⭐ **Live** | 6 | [live.md](live.md) | [live.th.md](live.th.md) |
| ⭐⭐ **ส่วนที่ตัดขวางทุก domain** | 7 | [cross-cutting.md](cross-cutting.md) | [cross-cutting.th.md](cross-cutting.th.md) |

⭐ คือจุดที่มีงานวิศวกรรมน่าสนใจ ส่วน ⭐⭐ คือสองไฟล์ที่ควรอ่าน ถ้าจะอ่านแค่สองไฟล์

## ถ้าจะดูแค่สี่รูป

สี่รูปนี้คือสิ่งที่เดาไม่ได้จากรายการ endpoint และเป็นเหตุผลว่าทำไมระบบถึงมีรูปร่างแบบนี้

1. **[การสั่งซื้อ](order.th.md#-การสั่งซื้อ--saga)** — สอง service สอง database
   ไม่มี transaction ให้ใช้ จึงต้องใช้ saga: จอง เขียน แล้วชดเชยถ้าพัง
2. **[Transactional outbox](cross-cutting.th.md#-outbox-และ-relay-ของมัน)** —
   วิธี publish event โดยไม่มีช่องว่างที่ write สำเร็จแต่ publish ไม่สำเร็จ
3. **[การจอง stock](product.th.md#-จอง-stock-grpc--mutual-tls)** — atomic conditional
   update ครั้งเดียวแทนการใช้ lock ผ่าน gRPC ที่ป้องกันด้วย mutual TLS
4. **[Trace ที่รอดข้าม outbox](cross-cutting.th.md#-trace-ที่รอดข้าม-outbox)** —
   request จบไปแล้วตอน event ถูก publish จึงต้องเก็บ trace context ไว้แล้วเอากลับมาใช้

## วิธีอ่านสัญลักษณ์

- `actor` คืออยู่นอกระบบ ส่วน `participant` คืออยู่ในระบบ
- ลูกศรทึบ `->>` คือการเรียก ลูกศรประ `-->>` คือค่าที่คืนกลับ
- `alt` / `else` คือการแตกทาง, `opt` คือทางที่อาจไม่เกิดขึ้น, `loop` คือการวนซ้ำ,
  `par` คือการทำงานพร้อมกันจริง ๆ
- อะไรก็ตามที่วาด **หลัง** จากที่ response ถูกส่งกลับไปหา client แล้ว
  คืองานที่ทำบน background loop ความต่างตรงนี้คือสาระทั้งหมดของ diagram ชุด outbox
  จึงต้องวาดออกมา ไม่ใช่แค่เขียนอธิบาย

## การรักษาให้ตรงกับโค้ด

diagram ที่ขัดกับโค้ด แย่กว่าการไม่มี diagram เลย เพราะคนจะเชื่อมัน
ชุดนี้วาดจากโค้ดที่เป็นอยู่จริง ไม่ใช่จากดีไซน์ที่ตั้งใจไว้ และรายการ endpoint
ตรงกับ [`../../postman/`](../../postman/) ซึ่งรันได้จริง มันจึงเพี้ยนแบบเงียบ ๆ ไม่ได้

อ่านต่อ: [domains.md](../domains.md) สำหรับภาพรวม topology,
[tech-stack.md](../tech-stack.md) สำหรับเหตุผลของแต่ละเทคโนโลยี,
[user-domain-design.md](../user-domain-design.md) สำหรับ user story ที่ diagram
ของ User มาจาก และ [transactional-outbox.th.md](../transactional-outbox.th.md)
สำหรับคำอธิบาย outbox แบบร้อยแก้ว
