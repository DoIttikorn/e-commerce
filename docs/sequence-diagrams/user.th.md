# User — UML Sequence Diagram

*English: [user.md](user.md) · สารบัญ: [README.th.md](README.th.md)*

domain แรก และเป็นแม่แบบที่ domain อื่นลอกตามทั้งหมด มี driving adapter สองตัว —
REST และ gRPC — ครอบ service ตัวเดียว โดยไม่มีการแตก if ข้างใน service เลย

| Flow | Endpoint |
|---|---|
| [สมัครสมาชิก](#สมัครสมาชิก) | `POST /api/v1/auth/register` |
| [เข้าสู่ระบบ](#เข้าสู่ระบบ) | `POST /api/v1/auth/login` |
| [ตรวจ Bearer token](#ตรวจ-bearer-token-ทุกเส้นที่ต้องล็อกอิน) | ทุกเส้นที่ต้องล็อกอิน |
| [รายการผู้ใช้](#รายการผู้ใช้) | `GET /api/v1/users` |
| [ดูผู้ใช้รายเดียว](#ดูผู้ใช้รายเดียว) | `GET /api/v1/users/{id}` |
| [สร้างผู้ใช้](#สร้างผู้ใช้) | `POST /api/v1/users` |
| [แก้ไข](#แก้ไข-patch-ไม่ใช่-put) | `PATCH /api/v1/users/{id}` |
| [ลบ](#ลบ) | `DELETE /api/v1/users/{id}` |
| [gRPC](#grpc-createuser-และ-getuser) | `user.v1.UserService/*` |

---

## สมัครสมาชิก

จุดที่น่าสนใจคือกิ่งที่ fail — ความไม่ซ้ำของอีเมลถูกบังคับด้วย **unique index ใน MongoDB**
ไม่ใช่การ query ระดับ application ว่า "อีเมลนี้มีแล้วหรือยัง" ก่อน insert เพราะการเช็คแบบนั้น race
ถ้าสมัครพร้อมกันสองคน ทั้งคู่จะผ่านการเช็คและ insert ทั้งคู่

```mermaid
sequenceDiagram
    autonumber
    actor C as ผู้ใช้
    participant H as handler
    participant S as user.Service
    participant R as mongodb adapter
    participant DB as MongoDB

    C->>H: POST /api/v1/auth/register
    H->>H: ถอดรหัส body และตรวจความถูกต้อง
    alt ข้อมูลไม่ผ่าน
        H-->>C: 400 validation failed พร้อม fields
    else ผ่าน
        H->>S: Register(ctx, NewUser)
        S->>S: hash รหัสผ่านด้วย bcrypt
        S->>R: Create(ctx, User)
        R->>DB: insertOne ที่ users
        alt ชน unique index E11000
            DB-->>R: write error
            R-->>S: ErrEmailTaken
            S-->>H: ErrEmailTaken
            H-->>C: 409 email already registered
        else insert สำเร็จ
            DB-->>R: inserted id
            R-->>S: User
            S-->>H: User
            H-->>C: 201 user โดยไม่มี password hash เด็ดขาด
        end
    end
```

## เข้าสู่ระบบ

อีเมลที่ไม่มีในระบบ กับ รหัสผ่านผิด เดินคนละเส้นทางข้างใน แต่คืน 401 ที่ **เหมือนกันเป๊ะ**
เพื่อไม่ให้ใช้หน้า login ไล่เดาว่าอีเมลไหนสมัครไว้แล้ว ส่วนการเทียบ bcrypt ช้าโดยตั้งใจ
และเป็นเหตุผลที่ endpoint login วัดได้ 63 req/s เทียบกับแคตตาล็อกที่ 13,818 req/s

```mermaid
sequenceDiagram
    autonumber
    actor C as ผู้ใช้
    participant H as handler
    participant S as user.Service
    participant R as mongodb adapter
    participant T as auth issuer

    C->>H: POST /api/v1/auth/login
    H->>S: Login(ctx, email, password)
    S->>R: ByEmail(ctx, email)
    alt ไม่มีผู้ใช้นี้
        R-->>S: ErrUserNotFound
        S-->>H: ErrInvalidCredentials
    else เจอ
        R-->>S: User พร้อม password hash
        S->>S: เทียบ bcrypt
        alt hash ไม่ตรง
            S-->>H: ErrInvalidCredentials
        else ตรง
            S->>T: Issue(subject = user id)
            T-->>S: token ที่เซ็นแล้ว และเวลาหมดอายุ
            S-->>H: token
        end
    end
    alt ปฏิเสธ
        H-->>C: 401 invalid credentials เหมือนกันทั้งสองกรณี
    else ผ่าน
        H-->>C: 200 token และ expires_at
    end
```

## ตรวจ Bearer token (ทุกเส้นที่ต้องล็อกอิน)

การ verify จะ **ล็อก algorithm ก่อน** ที่จะเชื่อ claim ใด ๆ ถ้าไม่ทำ token ที่ประกาศว่า `none`
จะผ่านโดยไม่มีลายเซ็นเลย และ token ที่ประกาศ `RS256` สามารถถูกเซ็นด้วย public key
ในฐานะ HMAC secret ได้

```mermaid
sequenceDiagram
    autonumber
    actor C as ผู้ใช้
    participant MW as auth middleware
    participant V as auth verifier
    participant H as handler

    C->>MW: request พร้อม Authorization Bearer token
    alt ไม่มี header หรือไม่ใช่ Bearer
        MW-->>C: 401 unauthorized
    else มี
        MW->>V: Verify(token)
        V->>V: ตรวจว่า signing method ตรงก่อนเป็นอันดับแรก
        alt algorithm ผิด ลายเซ็นผิด หรือหมดอายุ
            V-->>MW: error
            MW-->>C: 401 unauthorized
        else ถูกต้อง
            V-->>MW: claims พร้อม subject
            MW->>MW: ใส่ subject ลงใน request context
            MW->>H: next.ServeHTTP
            H-->>C: response ของ handler เอง
        end
    end
```

## รายการผู้ใช้

```mermaid
sequenceDiagram
    autonumber
    actor C as ผู้ใช้
    participant H as handler
    participant S as user.Service
    participant R as mongodb adapter
    participant DB as MongoDB

    C->>H: GET /api/v1/users?limit=20&offset=0
    H->>H: จำกัด limit และ offset ให้อยู่ในช่วงที่สมเหตุสมผล
    H->>S: List(ctx, limit, offset)
    S->>R: List(ctx, limit, offset)
    par ยิงคนละรอบพร้อมกัน
        R->>DB: find พร้อม skip และ limit
        R->>DB: countDocuments
    end
    DB-->>R: หน้าข้อมูลและจำนวนรวม
    R-->>S: users และ total
    S-->>H: users และ total
    H-->>C: 200 users, total, limit, offset
```

## ดูผู้ใช้รายเดียว

ID ที่ **ผิดรูปแบบ** ได้ 400 ส่วน ID ที่รูปแบบถูกแต่ไม่มีของ ได้ 404
มันคนละความผิดพลาด และการยุบรวมกันทำให้ caller เสียข้อมูลที่ต้องใช้ตัดสินใจ

```mermaid
sequenceDiagram
    autonumber
    actor C as ผู้ใช้
    participant H as handler
    participant S as user.Service
    participant R as mongodb adapter
    participant DB as MongoDB

    C->>H: GET /api/v1/users/{id}
    H->>S: ByID(ctx, id)
    S->>R: ByID(ctx, id)
    alt ไม่ใช่ MongoDB ObjectID
        R-->>S: ErrInvalidID
        S-->>H: ErrInvalidID
        H-->>C: 400 malformed user id
    else รูปแบบถูก
        R->>DB: findOne ด้วย _id
        alt ไม่มี document
            DB-->>R: ErrNoDocuments
            R-->>S: ErrUserNotFound
            S-->>H: ErrUserNotFound
            H-->>C: 404 user not found
        else เจอ
            DB-->>R: document
            R-->>S: User
            S-->>H: User
            H-->>C: 200 user
        end
    end
```

## สร้างผู้ใช้

เขียนลง database เหมือนการสมัครทุกอย่าง ต่างกันที่สิทธิ์: เส้นนี้ต้องมี token
"สมัครให้ฉัน" กับ "เพิ่มผู้ใช้เข้าระบบ" เป็นคนละ operation ถึงจะได้ row หน้าตาเดียวกัน

```mermaid
sequenceDiagram
    autonumber
    actor C as ผู้ใช้
    participant MW as auth middleware
    participant H as handler
    participant S as user.Service
    participant R as mongodb adapter

    C->>MW: POST /api/v1/users พร้อม bearer token
    MW->>H: request ที่ผ่านการยืนยันตัวตนแล้ว
    H->>H: ตรวจความถูกต้องของ body
    H->>S: Create(ctx, NewUser)
    S->>S: hash ด้วย bcrypt
    S->>R: Create(ctx, User)
    R-->>S: User หรือ ErrEmailTaken
    S-->>H: ผลลัพธ์
    H-->>C: 201 user หรือ 409 ถ้าอีเมลซ้ำ
```

## แก้ไข (PATCH ไม่ใช่ PUT)

โจทย์บอกให้แก้ชื่อ **หรือ** อีเมล แปลว่า field ที่ไม่ส่งมาต้องไม่ถูกแตะ
struct ที่เป็น string ธรรมดาแสดงออกไม่ได้ เพราะ "ไม่ส่งมา" กับ "ส่งมาเป็นค่าว่าง"
หน้าตาเหมือนกัน จึงใช้ `*string`: `nil` = ไม่ได้ส่ง, `""` = ตั้งใจให้เป็นค่าว่าง

```mermaid
sequenceDiagram
    autonumber
    actor C as ผู้ใช้
    participant MW as auth middleware
    participant H as handler
    participant S as user.Service
    participant R as mongodb adapter
    participant DB as MongoDB

    C->>MW: PATCH /api/v1/users/{id}
    MW->>H: subject อยู่บน context
    H->>H: requireSelf เทียบ subject กับ id ใน path
    alt เป็นของคนอื่น
        H-->>C: 403 forbidden
    else เป็นของตัวเอง
        H->>H: ถอดรหัสลง field ที่เป็น pointer
        alt ไม่ส่ง field มาเลยสักตัว
            H-->>C: 400 ต้องมีอย่างน้อยหนึ่ง field
        else มีอย่างน้อยหนึ่ง
            H->>S: Update(ctx, id, Update)
            S->>R: Update(ctx, id, Update)
            R->>DB: updateOne โดยใส่เฉพาะ field ที่ส่งมาใน $set
            alt อีเมลใหม่ไปชนกับคนอื่น
                DB-->>R: duplicate key
                R-->>S: ErrEmailTaken
                H-->>C: 409 email already registered
            else แก้สำเร็จ
                DB-->>R: document ที่แก้แล้ว
                R-->>S: User
                S-->>H: User
                H-->>C: 200 user
            end
        end
    end
```

## ลบ

```mermaid
sequenceDiagram
    autonumber
    actor C as ผู้ใช้
    participant MW as auth middleware
    participant H as handler
    participant S as user.Service
    participant R as mongodb adapter

    C->>MW: DELETE /api/v1/users/{id}
    MW->>H: subject อยู่บน context
    H->>H: requireSelf
    alt เป็นของคนอื่น
        H-->>C: 403 forbidden
    else เป็นของตัวเอง
        H->>S: Delete(ctx, id)
        S->>R: Delete(ctx, id)
        R-->>S: สำเร็จ หรือ ErrUserNotFound
        S-->>H: ผลลัพธ์
        H-->>C: 204 no content หรือ 404
    end
    Note over C,H: token ยังใช้ได้จนกว่าจะหมดอายุ<br/>token แบบ stateless เพิกถอนก่อนกำหนดไม่ได้<br/>เว้นแต่จะเอาการค้น session<br/>ที่มันเกิดมาเพื่อหลีกเลี่ยง กลับมาใส่ใหม่
```

## gRPC: CreateUser และ GetUser

adapter สองตัวครอบ service ตัวเดียวคือหัวใจของ hexagon: `gapi` และ `handler`
เรียก `user.Service` ตัวเดียวกันเป๊ะ และ service ไม่รู้จักทั้ง HTTP status code
และ gRPC code — **adapter แต่ละตัวเป็นเจ้าของการแปลง error ของตัวเอง**

gRPC มีแค่สองเส้นนี้โดยตั้งใจ `ListUsers` และ `Login` เป็น anti-pattern ตรงนี้
เพราะ service ต้องไม่ยืนยันตัวตน *ในฐานะ* ผู้ใช้ แต่ต้องส่งต่อ token ของผู้ใช้ไป

```mermaid
sequenceDiagram
    autonumber
    participant Svc as service ที่เรียกเข้ามา
    participant I as gapi interceptor
    participant G as gapi server
    participant S as user.Service
    participant R as mongodb adapter

    Svc->>I: CreateUser หรือ GetUser พร้อม token ใน metadata
    Note over I: token มาจาก gRPC metadata<br/>ไม่ใช่ Authorization header
    alt ไม่มี metadata หรือ token ไม่ถูกต้อง
        I-->>Svc: Unauthenticated
    else ถูกต้อง
        I->>G: handler พร้อม subject บน context
        G->>G: ตรวจความถูกต้องของ request message
        alt ไม่ผ่าน
            G-->>Svc: InvalidArgument พร้อม errdetails.BadRequest
        else ผ่าน
            G->>S: เมธอดตัวเดียวกับที่ REST handler เรียก
            S->>R: เรียก repository
            R-->>S: entity หรือ sentinel error
            S-->>G: entity หรือ sentinel error
            alt ErrEmailTaken
                G-->>Svc: AlreadyExists
            else ErrUserNotFound
                G-->>Svc: NotFound
            else ErrInvalidID
                G-->>Svc: InvalidArgument
            else สำเร็จ
                G-->>Svc: user message โดยไม่มี field password
            end
        end
    end
```
