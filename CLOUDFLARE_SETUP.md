# Cloudflare Setup Guide: Worker VPS Custom Subdomain (Port 2052 Hide & HTTPS)

এই গাইডের মাধ্যমে আপনি যেকোনো Worker VPS-এর স্ট্রিম ইউআরএল থেকে `:2052` পোর্ট লুকিয়ে ফেলে সরাসরি ক্লিন HTTPS সাবডোমেইনে (যেমন: `https://vps1.yourdomain.com`) রূপান্তর করতে পারবেন—আপনার মূল ডোমেইন বা অন্য সাবডোমেইনের গ্লোবাল SSL সেটিংস পরিবর্তন না করেই।

---

## 🎯 লক্ষ্য
- **ইউআরএল রূপান্তর:** `http://YOUR_VPS_IP:2052` ➡️ `https://vps1.yourdomain.com`
- **কোনো পোর্ট দৃশ্যমান থাকবে না** (`:2052` হাইড থাকবে)।
- **স্বয়ংক্রিয় ফ্রি SSL (HTTPS)** সক্রিয় থাকবে।
- **মূল ডোমেইনের গ্লোবাল SSL মোড নষ্ট হবে না** (আলাদা আলাদা থাকবে)।

---

## ধাপ ১: Cloudflare DNS সেটআপ (Proxy On)
1. [dash.cloudflare.com](https://dash.cloudflare.com)-এ লগইন করে আপনার ডোমেইন সিলেক্ট করুন।
2. বাঁদিকের মেনু থেকে **DNS** > **Records**-এ যান।
3. **Add record** বাটনে ক্লিক করুন:
   - **Type:** `A`
   - **Name:** `vps1` (বা আপনার পছন্দের সাবডোমেইন নাম)
   - **IPv4 address:** আপনার Worker VPS-এর IP (যেমন: `YOUR_VPS_IP` / `203.0.113.10`)
   - **Proxy status:** **Proxied (কমলা মেঘ ☁️ On রাখবেন)**
   - **TTL:** `Auto`
4. **Save** করুন।

---

## ধাপ ২: Origin Rule তৈরি করুন (পোর্ট ২০৫২ লুকানো)
ব্রাউজারের রিকোয়েস্ট ব্যাকএন্ডে VPS-এর `2052` পোর্টে রিডাইরেক্ট ছাড়া ফরওয়ার্ড করার জন্য:

1. Cloudflare ড্যাশবোর্ডের মেনু থেকে **Rules** > **Origin Rules**-এ যান।
2. **Create rule** বাটনে ক্লিক করুন:
   - **Rule name:** `VPS1 Port Rewrite` (বা যেকোনো নাম)
   - **If incoming requests match... (Field Selector):**
     - **Field:** `Hostname`
     - **Operator:** `equals`
     - **Value:** `vps1.yourdomain.com` *(আপনার সাবডোমেইনটি লিখুন)*
   - **Destination Port:**
     - সিলেক্ট করুন: **Rewrite to...**
     - ঘরে লিখুন: **`2052`**
3. নিচে **Deploy** বাটনে ক্লিক করুন।

---

## ধাপ ৩: শুধুমাত্র এই সাবডোমেইনের জন্য SSL Flexible করা (Configuration Rule)
মূল ডোমেইনের `Full` বা `Full (strict)` SSL অক্ষুণ্ণ রেখে শুধুমাত্র Worker VPS সাবডোমেইনের জন্য SSL `Flexible` করার নিয়ম:

1. বাঁদিকের মেনু থেকে **Rules** > **Configuration Rules**-এ যান।
2. **Create rule** বাটনে ক্লিক করুন:
   - **Rule name:** `VPS1 Flexible SSL`
   - **If incoming requests match... (Field Selector):**
     - **Field:** `Hostname`
     - **Operator:** `equals`
     - **Value:** `vps1.yourdomain.com` *(আপনার সাবডোমেইনটি লিখুন)*
   - **Action Settings (তালিকা থেকে খুঁজুন):**
     - টিক দিন: **SSL/TLS encryption**
     - ড্রপডাউন থেকে সিলেক্ট করুন: **`Flexible`**
3. নিচে **Deploy** বাটনে ক্লিক করুন।

---

## ধাপ ৪: ক্যাশিং ও বাফারিং পলিসি (প্রস্তাবিত)
ভিডিও স্ট্রিমিং ও রেঞ্জ রিকোয়েস্ট দ্রুত ও নিরবচ্ছিন্ন রাখার জন্য:
- **ভিডিও ও মিডিয়া স্ট্রিম ক্যাশ করার প্রয়োজন নেই:** আমাদের সার্ভার ইতিমধ্যেই ব্রাউজারকে `Cache-Control: public, max-age=31536000, immutable` ও `Accept-Ranges: bytes` হেডার পাঠায়, ফলে ব্রাউজার নিজে থেকেই চাঙ্ক ক্যাশ করে।
- Cloudflare ডিফল্টভাবে ভিডিও ফাইল বাইপাস করে, যা বাফারিং এড়াতে সবচেয়ে সহায়ক।

*(ঐচ্ছিক)* আপনি চাইলে সাবটাইটেল বা মেটাডেটার জন্য **Caching** > **Cache Rules** থেকে `.vtt` ও `.json` ক্যাশ করতে পারেন।

---

## 🚀 ভেরিফিকেশন ও টেস্ট
Cloudflare রুল ডিপ্লয় করার ১–২ মিনিটের মধ্যে টার্মিনাল বা ব্রাউজারে টেস্ট করুন:

```bash
curl -I https://vps1.yourdomain.com/health
```

যদি রেসপন্সে `HTTP/2 200` বা স্ট্রিমিং রেসপন্স হেডার আসে, তবে আপনার ক্লাউডফ্লেয়ার সেটআপ সম্পূর্ণ সফল!
