# ส่วนที่ตัดขวางทุก domain — UML Sequence Diagram

*English: [cross-cutting.md](cross-cutting.md) · สารบัญ: [README.th.md](README.th.md)*

กลไกที่ทุก service ใช้ร่วมกัน ไม่มีอันไหนเป็นของ domain ใด domain หนึ่ง
และทั้งหมดนี้คือเหตุผลที่ domain ต่าง ๆ เรียบง่ายได้ขนาดนั้น

| Flow | อยู่ที่ |
|---|---|
| ⭐⭐ [outbox และ relay ของมัน](#-outbox-และ-relay-ของมัน) | `internal/outbox` |
| ⭐⭐ [trace ที่รอดข้าม outbox](#-trace-ที่รอดข้าม-outbox) | `internal/tracing` |
| ⭐ [ลำดับ middleware](#-ลำดับ-middleware) | `internal/middleware` |
| ⭐ [การจับมือของ mutual TLS](#-การจับมือของ-mutual-tls) | `internal/servicetls` |
| [liveness กับ readiness](#liveness-กับ-readiness) | `internal/appserver` |
| [การเริ่มระบบ](#การเริ่มระบบ) | `internal/appserver` |
| [การปิดระบบอย่างนุ่มนวล](#การปิดระบบอย่างนุ่มนวล) | `internal/appserver` |

---

## ⭐⭐ outbox และ relay ของมัน

*คำอธิบายแบบร้อยแก้ว: [transactional-outbox.th.md](../transactional-outbox.th.md)*

ปัญหาที่มันเกิดมาเพื่อกำจัด วาดไว้เป็นครึ่งบนของรูป: service ที่เขียนเสร็จแล้วค่อย publish
จะมี **ช่องว่าง** ถ้า crash ตรงนั้น หรือ broker ปฏิเสธ การเปลี่ยนแปลงเกิดขึ้นจริง
แต่ไม่มีใครถูกบอก และการคืน error ก็ไม่ช่วย เพราะ write commit ไปแล้ว
event จึงหายไปเฉย ๆ และระบบที่ต้องใช้มันก็ค่อย ๆ เพี้ยนออกจากกันโดยไม่มีอะไรบ่งบอก

การเขียน event ลงใน transaction เดียวกันกำจัดช่องว่างนั้นทิ้งทั้งหมด
เหลือแค่การ publish ทีหลัง ซึ่งอนุญาตให้ช้าได้ retry ได้ และซ้ำได้
สิ่งที่แลกคือ **at-least-once แทน at-most-once** ซึ่งถูกด้านกว่า
เพราะ consumer ต้องรับมือกับการซ้ำอยู่แล้ว ในเมื่อ Kafka ก็ให้การรับประกันแบบเดียวกัน

```mermaid
sequenceDiagram
    autonumber
    actor C as ผู้ใช้
    participant S as service ของ domain ใดก็ได้
    participant R as mongodb adapter
    participant DB as MongoDB
    participant RL as outbox relay
    participant K as Kafka

    rect rgb(255, 235, 235)
        Note over C,K: สิ่งที่มันมาแทนที่ — publish จาก request path
        C->>S: request ที่มีการเขียน
        S->>DB: commit การเปลี่ยนแปลง
        S--xK: publish พัง หรือ process ตายตรงนี้
        Note over S,K: การเปลี่ยนแปลงเกิดขึ้นจริง ไม่มีใครถูกบอก<br/>การทำให้ request พังก็เท่ากับ<br/>รายงานความสำเร็จว่าเป็นความล้มเหลว
    end

    rect rgb(240, 255, 240)
        Note over C,DB: สิ่งที่ทำแทน
        C->>S: request ที่มีการเขียน
        S->>S: ประกอบ event ไปพร้อมกับการเปลี่ยนแปลง
        S->>R: Save(ctx, entity, events)
        R->>DB: เริ่ม transaction
        R->>DB: เขียน entity
        R->>DB: insert event ลง outbox
        alt มีอะไรพัง
            DB-->>R: abort — ไม่มีการเปลี่ยนแปลง และไม่มี event
            Note over R,DB: สองอย่างนี้ขัดแย้งกันเองไม่ได้<br/>ซึ่งคือสาระทั้งหมด
        else
            DB-->>R: commit — ได้ทั้งคู่ หรือไม่ได้เลย
        end
        S-->>C: response
    end

    rect rgb(240, 248, 255)
        Note over RL,K: relay ที่ทำงานบน background loop
        loop วนตลอด
            RL->>DB: findOneAndUpdate แถวเก่าสุดที่ยังไม่ publish ตั้ง claimed_at
            Note over RL,DB: เป็น lease เพื่อไม่ให้ relay สองตัว<br/>publish แถวเดียวกันพร้อมกัน
            alt ไม่มีอะไรค้าง
                RL->>RL: หลับหนึ่งวินาทีแล้วมาดูใหม่
            else จองแถวได้
                RL->>K: PublishRaw topic, key, bytes ที่เก็บไว้
                Note over RL,K: ส่งเป็น raw — ถ้า encode ใหม่ตรงนี้<br/>การแก้ event type ทีหลังจะไปเขียน event<br/>ที่บันทึกไว้ก่อนหน้านั้นใหม่แบบเงียบ ๆ
                alt broker ล่ม
                    K--xRL: error
                    Note over RL,K: ปล่อยให้ยังจองอยู่ พอ lease หมดอายุก็ลองใหม่<br/>ซึ่งเป็นเหตุผลที่ broker ล่มแค่ทำให้ส่งช้า<br/>ไม่ใช่ทำให้ข้อมูลหาย
                else publish สำเร็จ
                    RL->>DB: ตั้ง published_at
                    Note over RL,DB: กรณี publish แล้วแต่ยังไม่ได้บันทึกก็เป็นไปได้<br/>แล้วมันจะ publish ซ้ำทีหลัง นั่นคือขอบของ<br/>at-least-once และเป็นเหตุผลที่ key สำคัญ
                end
            end
        end
    end
```

## ⭐⭐ trace ที่รอดข้าม outbox

request ID เชื่อมโยง log **ภายใน** process เดียว มันข้าม gRPC call ไม่ได้
และยิ่งข้าม event ที่เขียนตอน request แล้ว publish หลัง request จบไปแล้วหนึ่งวินาทีไม่ได้เลย

นี่คือรูปของส่วนที่ยากที่สุด span ที่เขียน event จบไปแล้วตอนที่ relay ทำงาน
trace context จึงถูก **เก็บไว้ใน outbox row** แล้วเอากลับมาใช้
การให้ parent เป็น span ที่จบไปแล้วเป็นเรื่องตั้งใจและถูกกฎ:
trace คือสายเหตุ-ผล ไม่ใช่ call stack

```mermaid
sequenceDiagram
    autonumber
    actor C as ผู้ใช้
    participant MW as tracing middleware
    participant S as service ของ domain
    participant O as outbox.Append
    participant DB as MongoDB
    participant RL as outbox relay
    participant K as Kafka
    participant Cn as consumer ใน service อื่น

    C->>MW: request อาจมี header traceparent มาด้วย
    MW->>MW: extract — เข้าร่วม trace ของผู้เรียก ไม่ใช่เริ่มอันใหม่
    Note over MW: service ที่ทิ้ง trace context ที่รับมา<br/>คือการเจาะรูใน trace ของคนอื่น
    MW->>MW: เริ่ม server span
    MW->>S: จัดการ request โดยมี span อยู่บน context

    S->>O: Append(ctx, events)
    O->>O: inject trace context ที่มีอยู่ลงใน carrier
    O->>DB: insert แถว outbox โดยมี trace_context ไปด้วย
    Note over O,DB: ต้องเก็บ เพราะอีกสักครู่<br/>context นี้จะไม่มีอยู่ที่ไหนอีกแล้ว

    S-->>MW: เสร็จ
    MW->>MW: เปลี่ยนชื่อ span เป็น METHOD /route-pattern
    Note over MW: เปลี่ยนชื่อหลัง router จับคู่เส้นทางแล้ว ชื่อจึงเป็น<br/>"GET /api/v1/users/{id}" ไม่ใช่หนึ่งชื่อ span<br/>ต่อหนึ่ง user id — กฎ cardinality เดียวกับ<br/>label ของ Prometheus ซึ่งเป็นเหตุผลที่<br/>middleware นี้เขียนเอง ไม่ใช้ otelhttp
    MW-->>C: response
    Note over C,MW: request จบแล้ว context ของมันหายไปแล้ว

    RL->>DB: จองแถว พร้อม trace_context ที่ติดมา
    RL->>RL: ดึง context นั้นกลับออกมา
    RL->>RL: เริ่ม producer span ที่มี parent เป็น request เดิม
    Note over RL: parent ของมันจบไปหลายวินาทีแล้ว ซึ่งถูกกฎ<br/>และเป็นสิ่งที่ Kafka instrumentation ทุกตัวทำ<br/>พก outbox.lag_ms ไปด้วย ว่ารอนานแค่ไหน
    RL->>K: publish โดย inject traceparent ลงใน HEADER ของข้อความ
    Note over RL,K: ใส่ใน header ไม่ใช่ payload เพราะ payload<br/>คือสัญญาของ domain — consumer ที่ไม่รู้จัก<br/>tracing เลยก็ยังต้องอ่านมันได้

    K->>Cn: ข้อความ อีกหลายนาทีต่อมา ใน process อื่น
    Cn->>Cn: extract จาก header แล้วเริ่ม consumer span
    Note over Cn: ตอนนี้ trace เดียวครอบคลุมทั้งการสั่งซื้อ<br/>การ publish และการอัปเดต projection<br/>แทนที่จะเป็น log สามกอง<br/>ที่ต้องเอา timestamp มาเทียบกันเอง
```

## ⭐ ลำดับ middleware

ลำดับไม่ได้สุ่มมา **RequestID ต้องมาก่อน** ไม่งั้นทุกอย่างที่อยู่ถัดไปจะบันทึกบรรทัด
ที่เชื่อมโยงอะไรไม่ได้ และ **Tracing ต้องมาก่อน Logging** เพื่อให้ `trace_id`
อยู่บน context ก่อนที่อะไรจะเริ่ม log

การเชื่อมโยงเกิดขึ้นใน **slog handler** ไม่ใช่ที่จุดเรียกแต่ละจุด
โค้ดใดก็ตามที่ log ด้วย request context จะถูกเชื่อมโยงให้ฟรี รวมถึงโค้ดใน domain
ที่ไม่รู้จัก HTTP เลย ทางเลือกอีกทางคือส่ง logger ผ่าน argument ไปเรื่อย ๆ ซึ่งแย่กว่า

```mermaid
sequenceDiagram
    autonumber
    actor C as ผู้ใช้
    participant ID as RequestID
    participant T as Tracing
    participant M as Metrics
    participant L as Logging
    participant Rt as chi router
    participant H as handler

    C->>ID: request
    ID->>ID: ใช้ X-Request-ID ที่ส่งมา หรือสร้างใหม่
    Note over ID: ค่าที่ส่งเข้ามาที่ยาวเกิน 64 bytes<br/>หรือมีอักขระที่ไม่ใช่ printable ASCII จะถูกทิ้ง<br/>เพราะมันไปจบใน log จึงเป็นช่องทาง log forging
    ID->>T: ctx พก request id ไปด้วย
    T->>T: extract traceparent แล้วเริ่ม server span
    T->>M: ctx พก trace ไปด้วยแล้ว
    M->>M: เริ่มจับเวลา และเตรียมที่เก็บ route pattern
    M->>L: ส่งต่อ
    L->>Rt: ส่งต่อ
    Rt->>Rt: จับคู่เส้นทาง แล้ว SetPattern ลงบน request
    Rt->>H: handler ทำงาน
    H-->>Rt: status และ body
    Rt-->>L: ย้อนกลับ
    L->>L: log method, path, status, duration พร้อม request_id และ trace_id
    L-->>M: ย้อนกลับ
    M->>M: ใส่ label ด้วย ROUTE PATTERN ไม่ใช่ r.URL.Path
    Note over M: path ดิบจะสร้าง time series หนึ่งเส้นต่อหนึ่ง id<br/>ส่วน request ที่ไม่ตรงเส้นทางไหนจะยุบเป็น "unmatched"<br/>เพื่อไม่ให้ scanner ทำแบบเดียวกันได้
    M-->>T: ย้อนกลับ
    T->>T: เปลี่ยนชื่อ span เป็น route pattern และตั้งสถานะ
    Note over T: เฉพาะ 5xx เท่านั้นที่ทำให้ span เป็น error<br/>404 หรือ 422 คือ server ทำงานถูกต้อง
    T-->>ID: ย้อนกลับ
    ID-->>C: response พร้อมสะท้อน X-Request-ID กลับไป
```

## ⭐ การจับมือของ mutual TLS

การยืนยันตัวตนระหว่าง service ไม่ใช่ปัญหาเดียวกับการยืนยันตัวตนผู้ใช้
และ bearer token ของผู้ใช้เป็นเครื่องมือที่ผิดสำหรับงานนี้ เพราะตอน Order จองสต็อก
ไม่มี user อยู่เลย และการยืม token ของใครมาใช้ แปลว่า Order service ที่ถูกเจาะ
จะกระทำการในนามของผู้ซื้อคนไหนก็ได้ที่บังเอิญกำลังเช็คเอาต์อยู่

```mermaid
sequenceDiagram
    autonumber
    participant O as Order ฝั่ง client
    participant OT as TLS ฝั่ง client
    participant PT as TLS ฝั่ง server
    participant P as Product ฝั่ง server

    O->>OT: เชื่อมต่อไป product:9090
    OT->>PT: ClientHello อย่างต่ำ TLS 1.3
    PT-->>OT: ServerHello, certificate ของ server, และขอ certificate จาก client
    OT->>OT: ตรวจ server กับ CA pool เฉพาะของระบบนี้
    Note over OT: สร้าง x509.NewCertPool ใหม่ ไม่ใช้ system root<br/>เพราะการเชื่อ CA สาธารณะตรงนี้<br/>จะทำลายความหมายของทั้งหมดนี้ทิ้ง
    alt ชื่อ server ไม่อยู่ใน certificate
        OT--xPT: ยกเลิก — นี่ไม่ใช่คนที่มันอ้างว่าเป็น
    else ชื่อตรง
        OT->>PT: certificate ของ client โดย CN = order
        PT->>PT: RequireAndVerifyClientCert เทียบกับ CA เดียวกัน
        alt ไม่ได้ยื่น certificate มา
            PT--xOT: handshake ล้มเหลว
            Note over PT: ถ้าไม่มีข้อนี้ อะไรก็ตามที่ส่ง packet<br/>มาถึง port นี้ได้ ก็จองสินค้าได้
        else เซ็นโดย CA ที่ไม่เกี่ยวข้อง
            PT--xOT: handshake ล้มเหลว
        else ถูกต้อง
            PT->>P: RPC ถึงโค้ดระดับ application ในที่สุด
            P-->>O: response ผ่านช่องทางที่สถาปนาแล้ว
        end
    end
```

## liveness กับ readiness

ความต่างสำคัญกว่าที่เห็น — **liveness** probe ของ Kubernetes ที่ fail จะ restart pod ทิ้ง
การเช็ค dependency ตรงนั้นจึงเปลี่ยน MongoDB ที่สะดุดแป๊บเดียว
ให้กลายเป็น restart loop พร้อมกันทุก instance — จากแค่ช้าลง กลายเป็นล่มทั้งระบบ
ส่วน **readiness** probe ที่ fail จะถอน instance ออกจาก load balancer
แต่ปล่อยให้รันต่อเพื่อฟื้นตัว

```mermaid
sequenceDiagram
    autonumber
    participant K as Kubernetes หรือ load balancer
    participant A as appserver
    participant DB as MongoDB
    participant Rd as Redis

    rect rgb(240, 255, 240)
        K->>A: GET /healthz
        A-->>K: 200 เสมอ ตราบใดที่ process ยังรัน
        Note over A: ไม่เช็คอะไรเลย ห้ามเพิ่มการเช็ค dependency<br/>ตรงนี้เด็ดขาด — database สะดุดแป๊บเดียว<br/>จะกลายเป็น restart loop ทั้ง cluster
    end

    rect rgb(240, 248, 255)
        K->>A: GET /readyz
        A->>A: ตั้ง timeout 2 วินาที ครอบทุกการเช็คที่ลงทะเบียนไว้
        par ทุกการเช็คที่ลงทะเบียนไว้
            A->>DB: Ping
        and เฉพาะที่ domain นั้นลงทะเบียนไว้
            A->>Rd: Ping
        end
        alt มีการเช็คไหน fail
            A-->>K: 503 พร้อมระบุชื่อ dependency ที่พัง
            Note over K,A: ถูกถอนออกจาก load balancer<br/>แต่ยังรันอยู่ จึงกลับเข้า pool ได้เอง<br/>เมื่อ dependency กลับมา
        else ผ่านหมด
            A-->>K: 200 ready
        end
    end
```

## การเริ่มระบบ

fail ให้เร็ว และรายงาน **ทุก** ปัญหาพร้อมกัน — deployment ที่ตั้งค่าผิด
ไม่ควรต้องแก้ทีละตัวแปรต่อการ restart หนึ่งครั้ง

```mermaid
sequenceDiagram
    autonumber
    participant M as cmd/<service>/main
    participant Cfg as config
    participant Tr as tracing
    participant DB as MongoDB
    participant K as Kafka
    participant A as appserver

    M->>Cfg: Load()
    Cfg->>Cfg: เก็บทุกปัญหา ไม่ใช่แค่ตัวแรก
    alt มีอะไรขาดหรือผิด
        Cfg-->>M: error เดียวที่ระบุครบทุกข้อ
        M->>M: พิมพ์ออก stderr แล้ว exit 1
        Note over M,Cfg: ไม่มีค่า default ให้ JWT secret หรือ Mongo URI<br/>service ที่เริ่มทำงานด้วยค่าความปลอดภัยที่เดามา<br/>แย่กว่า service ที่ปฏิเสธจะเริ่มทำงาน
    else ผ่าน
        M->>Tr: Init — ติดตั้ง propagator ไม่ว่าจะมี collector หรือไม่
        Note over Tr: ถ้าไม่มี collector จะได้ no-op provider แต่ propagator<br/>ยังถูกติดตั้ง เพื่อให้ trace context ที่รับเข้ามา<br/>ถูกส่งต่อ ไม่ใช่ถูกทิ้ง
        M->>DB: เชื่อมต่อและ ping
        Note over M,DB: mongo.Connect ไม่เคยติดต่อ server จริง<br/>การ ping จึงเป็นสิ่งที่เปลี่ยน database ที่ติดต่อไม่ได้<br/>ให้กลายเป็นการ startup ที่ล้มเหลว
        M->>DB: EnsureIndexes ซึ่งเป็นของ adapter ประจำ domain
        opt service นี้ใช้ Kafka
            M->>K: EnsureTopic ทุก topic ที่มัน publish หรือ consume
        end
        M->>A: ลงทะเบียน ready check, background task, shutdown hook
        M->>A: Run — ให้บริการจนกว่าจะมีสัญญาณ
    end
```

## การปิดระบบอย่างนุ่มนวล

ลำดับเป็นเรื่องตั้งใจ: API ระบายก่อน เพื่อให้ request ชุดสุดท้ายยังวัดได้
ในขณะที่ endpoint metrics ยังเปิดให้ scrape ได้อีกรอบสุดท้าย
และ background task ต้องจบก่อนที่ client ของมันจะถูกตัดออกจากใต้เท้า

```mermaid
sequenceDiagram
    autonumber
    participant OS as SIGINT หรือ SIGTERM
    participant A as appserver
    participant API as API server
    participant G as gRPC server
    participant Adm as admin server
    participant T as background task
    participant Cl as closer

    OS->>A: สัญญาณ
    A->>A: ยกเลิก root context
    A->>API: Shutdown ด้วย SHUTDOWN_TIMEOUT
    Note over A,API: API ก่อน เพื่อให้ request ชุดสุดท้ายยังวัดได้<br/>ระหว่างที่ /metrics ยัง scrape ได้อีกครั้ง
    API-->>A: request ที่ค้างอยู่ระบายหมดแล้ว
    A->>G: GracefulStop
    G-->>A: RPC ที่เปิดอยู่ทำงานจนจบ
    A->>Adm: Shutdown
    A->>T: context ถูกยกเลิกไปแล้ว — relay, consumer, reaper, count logger
    T-->>A: ทุก loop คืนค่าแล้ว
    Note over A,T: task จบ **ก่อน** ที่ closer จะทำงาน<br/>จึงไม่มีอะไรค้างอยู่กลาง query<br/>ตอนที่ client ของมันถูกตัดการเชื่อมต่อ
    loop closer ย้อนลำดับที่ลงทะเบียนไว้
        A->>Cl: ปิด Kafka, ปิด Redis, flush trace, disconnect Mongo
    end
    A-->>OS: exit 0
```
