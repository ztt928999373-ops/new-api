const { createRequire } = require('node:module');
const path = require('node:path');
const fs = require('node:fs');
const assert = require('node:assert/strict');
const requireTools = createRequire(path.join(process.env.BATCHD_BROWSER_TOOLS, 'package.json'));
const { chromium } = requireTools('playwright-core');
const origin = 'http://127.0.0.1:5536';
(async () => {
  const browser = await chromium.launch({executablePath:process.env.BATCHD_CHROME,headless:true});
  const context = await browser.newContext({locale:'en-US',viewport:{width:1440,height:1100}});
  await context.route('**/*', route => new URL(route.request().url()).origin === origin ? route.continue() : route.abort());
  await context.addInitScript(() => localStorage.setItem('i18nextLng','en'));
  const page = await context.newPage(); page.setDefaultTimeout(30000);
  const leaks = [];
  context.on('response', async response => {
    if (response.url().startsWith(origin+'/api/')) {
      const body = await response.text().catch(()=>'');
      if (body.includes('synthetic-batchd-key-')) leaks.push(new URL(response.url()).pathname);
    }
  });
  try {
    const status = await (await context.request.get(origin+'/api/status')).json();
    assert.equal(status.data.version,process.env.BATCHD_EXPECTED_SHA);
    await page.goto(origin+'/sign-in');
    await page.getByLabel('Username or Email',{exact:true}).fill('batchd-root');
    await page.getByLabel('Password',{exact:true}).fill('Synthetic-BatchD-Password!');
    await page.getByRole('button',{name:'Sign in',exact:true}).click();
    await page.waitForURL('**/dashboard/overview'); await page.goto(origin+'/channels');
    await page.getByRole('button',{name:'Prepare secure OpenAI channel',exact:true}).click();
    await page.getByLabel('Name',{exact:true}).fill('Windows D browser');
    await page.getByLabel('Model',{exact:true}).fill('batchd-model');
    await page.getByLabel('New API Key').fill('synthetic-batchd-key-browser-one');
    const created=page.waitForResponse(r=>new URL(r.url()).pathname==='/api/channel/openai-integration'&&r.request().method()==='POST');
    await page.getByRole('button',{name:'Create disabled channel'}).click();
    assert((await(await created).json()).success);
    await page.getByRole('status').filter({hasText:'Credential saved.'}).waitFor();
    const inputID=await page.getByLabel('New API Key').getAttribute('id');
    assert(inputID);
    await page.waitForFunction(id=>document.getElementById(id)?.value==='',inputID);
    assert.equal(await page.getByLabel('New API Key').inputValue(),'');
    await page.getByLabel('New API Key').fill('synthetic-batchd-key-browser-two');
    const replaced=page.waitForResponse(r=>new URL(r.url()).pathname.startsWith('/api/channel/credentials/')&&r.request().method()==='PUT');
    await page.getByRole('button',{name:'Replace credential'}).click();
    assert((await(await replaced).json()).success);
    await page.getByRole('status').filter({hasText:'Credential saved.'}).waitFor();
    await page.waitForFunction(id=>document.getElementById(id)?.value==='',inputID);
    assert.equal(await page.getByLabel('New API Key').inputValue(),'');
    await page.getByRole('button',{name:'Revoke credential'}).click();
    await page.getByRole('alertdialog').getByRole('button',{name:'Continue'}).click();
    await page.getByRole('definition').filter({hasText:'revoked'}).waitFor();
    assert(await page.getByRole('button',{name:'Test credential connection'}).isDisabled());
    assert.equal(leaks.length,0);
    const state = await page.evaluate(()=>JSON.stringify({local:{...localStorage},session:{...sessionStorage},url:location.href,dom:document.body.innerHTML}));
    assert(!state.includes('synthetic-batchd-key-'));
    await page.screenshot({path:path.join(process.env.BATCHD_LOGS,'management-browser.png'),fullPage:true});
    fs.writeFileSync(path.join(process.env.BATCHD_LOGS,'management-browser.json'),JSON.stringify({sha:process.env.BATCHD_EXPECTED_SHA,passed:true,checks:['production frontend login','disabled secure channel creation','version replacement','revocation','no plaintext supplier key in responses or browser storage']},null,2));
  } catch(error) {
    await page.screenshot({path:path.join(process.env.BATCHD_LOGS,'management-browser-failure.png'),fullPage:true});
    throw error;
  } finally { await browser.close(); }
})().catch(error=>{console.error(error.message);process.exitCode=1});
