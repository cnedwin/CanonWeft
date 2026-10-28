// Real third-party renderer acceptance. Dependencies live outside the product:
// node epub-reader-compat.cjs <reader node_modules> <epub> <output prefix>
const { chromium, expect } = require("@playwright/test");
const fs = require("node:fs/promises");
const http = require("node:http");
const path = require("node:path");
const crypto = require("node:crypto");

(async () => {
  const [dependencies, input, output] = process.argv.slice(2);
  if (!dependencies || !input || !output)
    throw new Error("Three paths required");
  const epub = await fs.readFile(input);
  const assets = new Map([
    ["/book.epub", ["application/epub+zip", epub]],
    [
      "/epub.js",
      [
        "text/javascript",
        await fs.readFile(path.join(dependencies, "epubjs/dist/epub.min.js")),
      ],
    ],
    [
      "/jszip.js",
      [
        "text/javascript",
        await fs.readFile(path.join(dependencies, "jszip/dist/jszip.min.js")),
      ],
    ],
  ]);
  const html = `<!doctype html><html lang="zh-CN"><meta charset="utf-8">
    <title>EPUB 阅读兼容验收</title><style>
    body{margin:24px;font-family:system-ui;background:#f2f3f5;color:#26313f}
    h1{font-size:22px}#reader{background:white;margin-top:16px;width:900px;height:720px}
    select,button{padding:8px;margin-right:10px}p{font-size:14px}</style>
    <h1>第三方 EPUB 阅读验收</h1><p id="metadata"></p>
    <label>目录 <select id="toc"></select></label><button id="cover">封面</button>
    <div id="reader"></div><script src="/jszip.js"></script><script src="/epub.js"></script>
    <script>
    window.readerErrors=[];
    window.book=ePub('/book.epub');
    window.rendition=book.renderTo('reader',{width:900,height:720,spread:'none',flow:'paginated'});
    rendition.on('displayError',e=>readerErrors.push(String(e)));
    book.ready.then(async()=>{
      const meta=await book.loaded.metadata;
      document.querySelector('#metadata').textContent=meta.title+' · '+meta.creator;
      const navigation=await book.loaded.navigation;
      function add(items){for(const item of items){
        if(item.href){const option=document.createElement('option');option.value=item.href;option.textContent=item.label;document.querySelector('#toc').append(option);}
        if(item.subitems)add(item.subitems);
      }}
      add(navigation.toc);
      document.querySelector('#toc').onchange=e=>rendition.display(e.target.value);
      document.querySelector('#cover').onclick=()=>rendition.display(0);
      await rendition.display();window.readerReady=true;
    }).catch(e=>readerErrors.push(String(e)));
    </script></html>`;
  const server = http.createServer((req, res) => {
    if (req.url === "/") {
      res.writeHead(200, { "Content-Type": "text/html; charset=utf-8" });
      return res.end(html);
    }
    const item = assets.get(req.url);
    if (!item) {
      res.writeHead(404);
      return res.end();
    }
    res.writeHead(200, { "Content-Type": item[0] });
    res.end(item[1]);
  });
  await new Promise((resolve) => server.listen(0, "127.0.0.1", resolve));
  let browser;
  try {
    browser = await chromium.launch();
    const page = await browser.newPage({
      viewport: { width: 1024, height: 960 },
    });
    const errors = [];
    page.on("pageerror", (error) => errors.push(error.message));
    await page.goto(`http://127.0.0.1:${server.address().port}/`);
    await page.waitForFunction(() => window.readerReady === true);
    await expect(page.locator("#metadata")).toContainText("守灯人");
    const cover = page.frameLocator("iframe").getByRole("img");
    await expect
      .poll(() =>
        cover.evaluate((image) => image.complete && image.naturalWidth > 0),
      )
      .toBe(true);
    await page.screenshot({ path: `${output}-cover.png`, fullPage: true });
    await page.getByLabel("目录").selectOption({ label: "雨夜来信" });
    await expect(page.frameLocator("iframe").locator("body")).toContainText(
      "林舟在门外停了下来",
    );
    await page.screenshot({ path: `${output}-chapter.png`, fullPage: true });
    await page.getByLabel("目录").selectOption({ label: "书籍信息" });
    await expect(page.frameLocator("iframe").locator("body")).toContainText(
      "测试作者",
    );
    await expect(page.frameLocator("iframe").locator("body")).toContainText(
      "旧城守灯人追查失踪者",
    );
    await page.getByRole("button", { name: "封面", exact: true }).click();
    await expect
      .poll(() =>
        cover.evaluate((image) => image.complete && image.naturalWidth > 0),
      )
      .toBe(true);
    expect(errors).toEqual([]);
    expect(await page.evaluate(() => window.readerErrors)).toEqual([]);
    const result = {
      input: path.resolve(input),
      sha256: crypto.createHash("sha256").update(epub).digest("hex"),
      renderer: JSON.parse(
        await fs.readFile(
          path.join(dependencies, "epubjs/package.json"),
          "utf8",
        ),
      ).version,
      chromium: browser.version(),
      checked: [
        "actual EPUB ZIP opened",
        "cover decoded",
        "nested chapter navigation",
        "Chinese prose",
        "book metadata",
        "return to cover",
      ],
    };
    await fs.writeFile(`${output}.json`, JSON.stringify(result, null, 2));
    console.log(JSON.stringify(result));
  } finally {
    if (browser) await browser.close();
    await new Promise((resolve) => server.close(resolve));
  }
})().catch((error) => {
  console.error(error);
  process.exitCode = 1;
});
