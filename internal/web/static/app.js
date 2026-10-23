'use strict';

// ---------- helpers ----------

const $ = (sel) => document.querySelector(sel);

// h builds DOM nodes. Strings become text nodes, so server data is never
// parsed as HTML.
function h(tag, props, ...children) {
	const el = document.createElement(tag);
	for (const [key, value] of Object.entries(props || {})) {
		if (value === null || value === undefined || value === false) continue;
		if (key === 'class') el.className = value;
		else if (key === 'style') Object.assign(el.style, value);
		else if (key.startsWith('on')) el.addEventListener(key.slice(2), value);
		else if (value === true) el.setAttribute(key, '');
		else el.setAttribute(key, value);
	}
	for (const child of children.flat()) {
		if (child === null || child === undefined || child === false) continue;
		el.append(child instanceof Node ? child : document.createTextNode(String(child)));
	}
	return el;
}

const safeURL = (url) => (typeof url === 'string' && url.startsWith('https://') ? url : null);

// art sizes a Twitch CDN image URL and returns a CSS background value.
function art(url, w, hgt) {
	const safe = safeURL(url);
	if (!safe) return null;
	const sized = safe
		.replace('{width}', w * 2)
		.replace('{height}', hgt * 2)
		.replace(/-\d+x\d+(\.(?:jpg|png|jpeg))$/i, `-${w * 2}x${hgt * 2}$1`);
	return `url(${JSON.stringify(sized)})`;
}

function thumb(url, cls = 'thumb', w = 44, hgt = 60) {
	const bg = art(url, w, hgt);
	return h('span', { class: cls, style: bg ? { backgroundImage: bg } : null, 'aria-hidden': 'true' });
}

function fmtNumber(n) {
	return new Intl.NumberFormat().format(n);
}

function fmtClock(seconds) {
	seconds = Math.max(0, Math.round(seconds));
	const hrs = Math.floor(seconds / 3600);
	const mins = Math.floor((seconds % 3600) / 60);
	const secs = seconds % 60;
	const pad = (v) => String(v).padStart(2, '0');
	return hrs > 0 ? `${hrs}:${pad(mins)}:${pad(secs)}` : `${mins}:${pad(secs)}`;
}

function fmtSpan(ms) {
	const mins = Math.round(Math.abs(ms) / 60000);
	if (mins < 60) return `${mins}m`;
	const hrs = Math.floor(mins / 60);
	if (hrs < 48) return `${hrs}h ${mins % 60}m`;
	return `${Math.floor(hrs / 24)}d ${hrs % 24}h`;
}

function relative(iso) {
	const diff = new Date(iso).getTime() - Date.now();
	return diff >= 0 ? `in ${fmtSpan(diff)}` : `${fmtSpan(diff)} ago`;
}

function toast(message, isError = false) {
	const el = h('div', { class: isError ? 'toast error' : 'toast', role: isError ? 'alert' : 'status' }, message);
	$('#toasts').append(el);
	setTimeout(() => el.remove(), isError ? 6000 : 3500);
}

async function api(path, body) {
	const res = await fetch(path, {
		method: 'POST',
		headers: { 'Content-Type': 'application/json', 'X-Requested-With': 'TwitchMiner' },
		body: JSON.stringify(body ?? {}),
	});
	const data = await res.json().catch(() => ({}));
	if (!res.ok) throw Object.assign(new Error(data.error || res.statusText), { data });
	return data;
}

const storage = {
	get(key, fallback) {
		try {
			const v = localStorage.getItem(key);
			return v === null ? fallback : JSON.parse(v);
		} catch {
			return fallback;
		}
	},
	set(key, value) {
		try {
			localStorage.setItem(key, JSON.stringify(value));
		} catch {
			/* storage unavailable; filters just won't persist */
		}
	},
};

// ---------- state ----------

const state = {
	status: 'Starting...',
	login: null,
	settings: null,
	channels: [],
	inventory: [],
	progress: null,
	wanted: [],
	manual: { active: false },
	games: [],
	pubsub: null,
	version: '',
};

const invFilter = Object.assign({ status: 'all', link: 'all', search: '' }, storage.get('inventory-filter', {}));

// ---------- tabs ----------

const TABS = ['main', 'games', 'inventory', 'settings'];
let activeTab = TABS.includes(location.hash.slice(1)) ? location.hash.slice(1) : 'main';

function showTab(name, focus = false) {
	activeTab = name;
	for (const btn of document.querySelectorAll('.tab')) {
		const on = btn.dataset.tab === name;
		btn.setAttribute('aria-selected', String(on));
		btn.tabIndex = on ? 0 : -1;
		if (on && focus) btn.focus();
	}
	renderVisibility();
	history.replaceState(null, '', name === 'main' ? location.pathname : `#${name}`);
}

$('#tabs').addEventListener('click', (e) => {
	const btn = e.target.closest('.tab');
	if (btn) showTab(btn.dataset.tab);
});

$('#tabs').addEventListener('keydown', (e) => {
	if (e.key !== 'ArrowRight' && e.key !== 'ArrowLeft') return;
	const i = TABS.indexOf(activeTab) + (e.key === 'ArrowRight' ? 1 : -1);
	showTab(TABS[(i + TABS.length) % TABS.length], true);
});

const loggedIn = () => state.login?.state === 'logged_in';

function renderVisibility() {
	const ok = loggedIn();
	$('#login-view').hidden = ok;
	$('#tabs').hidden = !ok;
	for (const name of TABS) $(`#tab-${name}`).hidden = !ok || activeTab !== name;
}

// ---------- status bar ----------

function renderStatus() {
	$('#status').textContent = state.status;
}

function renderManual() {
	const m = state.manual || {};
	$('#manual').hidden = !m.active;
	$('#manual-text').textContent = m.active ? `Manual mode: ${m.game}${m.channel ? ` on ${m.channel}` : ''}` : '';
}

$('#manual-exit').addEventListener('click', () => api('/api/manual/exit').catch((err) => toast(err.message, true)));

function renderPubsub() {
	const p = state.pubsub;
	$('#pubsub').textContent = p && p.total ? `PubSub ${p.connected}/${p.total}` : '';
}

function setConnection(ok) {
	const el = $('#conn');
	el.classList.toggle('ok', ok);
	el.classList.toggle('down', !ok);
	$('#conn-text').textContent = ok ? 'Live' : 'Reconnecting';
}

// ---------- login ----------

function renderLogin() {
	const l = state.login || { state: 'checking' };
	const card = $('#login-card');
	const account = $('#account');
	account.hidden = !loggedIn();
	account.textContent = l.login || '';
	account.title = l.login ? `Logged in as ${l.login}` : '';
	$('#account-detail').textContent = l.login ? `Logged in as ${l.login} (user ID ${l.user_id}).` : '';

	if (l.state === 'code') {
		const link = safeURL(l.verification_uri) || 'https://www.twitch.tv/activate';
		card.replaceChildren(
			h('div', { class: 'card-header' },
				h('h1', null, 'Connect your Twitch account'),
				h('p', null, 'Open the Twitch activation page and enter this code. TwitchMiner continues automatically once you approve it.'),
			),
			h('div', { class: 'device-code', 'aria-label': 'Activation code' }, l.user_code),
			h('a', { class: 'btn', href: link, target: '_blank', rel: 'noopener noreferrer' }, 'Open twitch.tv/activate'),
			h('p', { class: 'login-wait' }, h('span', { class: 'spinner', 'aria-hidden': 'true' }),
				h('span', { id: 'login-expiry' }, 'Waiting for approval')),
		);
		tickLogin();
	} else if (l.state === 'error') {
		card.replaceChildren(
			h('div', { class: 'card-header' }, h('h1', null, 'Twitch login')),
			h('p', { class: 'login-error' }, l.error || 'Something went wrong.'),
			h('p', { class: 'login-wait' }, h('span', { class: 'spinner', 'aria-hidden': 'true' }), 'Retrying shortly...'),
		);
	} else {
		card.replaceChildren(
			h('div', { class: 'card-header' }, h('h1', null, 'Twitch login')),
			h('p', { class: 'login-wait' }, h('span', { class: 'spinner', 'aria-hidden': 'true' }), 'Checking your saved login...'),
		);
	}
	renderVisibility();
	renderPaused();
}

function tickLogin() {
	const el = document.getElementById('login-expiry');
	const l = state.login;
	if (!el || !l || l.state !== 'code' || !l.expires_at) return;
	const left = (new Date(l.expires_at).getTime() - Date.now()) / 1000;
	el.textContent = left > 0 ? `Waiting for approval · code expires in ${fmtClock(left)}` : 'Getting a new code...';
}

// ---------- now mining ----------

function progressRemaining(p) {
	const elapsed = Math.min(Math.max((Date.now() - p.updated_at) / 1000, 0), 59);
	return Math.max(p.remaining_minutes * 60 - elapsed, 0);
}

function renderProgress() {
	const root = $('#progress');
	const p = state.progress;
	if (!p) {
		root.replaceChildren(h('p', { class: 'empty' }, 'Nothing is being mined right now.'));
		return;
	}
	const pct = Math.round(p.progress * 100);
	const bg = art(p.benefit_image, 72, 72) || art(p.game_icon, 72, 96);
	const campaignURL = safeURL(p.campaign_url);
	root.replaceChildren(
		h('div', { class: 'progress-card' },
			h('span', { class: 'progress-art', style: bg ? { backgroundImage: bg } : null, 'aria-hidden': 'true' }),
			h('div', { class: 'progress-body' },
				h('div', { class: 'progress-title' }, p.rewards || p.drop_name),
				h('div', { class: 'progress-sub' },
					campaignURL ? h('a', { href: campaignURL, target: '_blank', rel: 'noopener noreferrer' }, p.campaign_name) : p.campaign_name,
					` · ${p.game_name}`,
					p.channel ? ` · on ${p.channel}` : '',
				),
				h('div', { class: 'bar', role: 'progressbar', 'aria-valuemin': '0', 'aria-valuemax': '100', 'aria-valuenow': String(pct), 'aria-label': 'Drop progress' },
					h('span', { class: 'bar-fill', style: { width: `${pct}%` } })),
				h('div', { class: 'progress-stats' },
					h('span', null, h('strong', null, `${p.current_minutes} / ${p.required_minutes}`), ' minutes'),
					h('span', null, h('strong', { class: 'countdown', id: 'countdown' }, fmtClock(progressRemaining(p))), ' left'),
				),
			),
		),
	);
}

function tickProgress() {
	const el = document.getElementById('countdown');
	if (el && state.progress) el.textContent = fmtClock(progressRemaining(state.progress));
}

// ---------- mining queue ----------

function renderWanted() {
	const root = $('#wanted');
	const list = state.wanted || [];
	$('#wanted-count').textContent = list.length ? `${list.length} game${list.length === 1 ? '' : 's'}` : '';
	if (!list.length) {
		const hint = state.settings && !state.settings.games_to_watch.length && state.settings.priority_list_only
			? 'Add games in the Games tab to start mining.'
			: 'No campaign has rewards left to mine.';
		root.replaceChildren(h('p', { class: 'empty' }, hint));
		return;
	}
	const current = state.progress?.game_name;
	root.replaceChildren(
		h('ol', { class: 'queue' },
			list.map((g, i) => {
				const drops = g.campaigns.flatMap((c) => c.drops.map((d) => d.benefits.join(', ') || d.name));
				const shown = drops.slice(0, 3).join(' · ') + (drops.length > 3 ? ` · +${drops.length - 3} more` : '');
				return h('li', { class: g.game_name === current ? 'queue-item current' : 'queue-item' },
					h('span', { class: 'queue-rank' }, i + 1),
					thumb(g.game_icon),
					h('div', { class: 'queue-body' },
						h('div', { class: 'queue-name' }, g.game_name, ' ',
							g.game_name === current ? h('span', { class: 'tag tag-pink' }, 'mining') : null, ' ',
							g.fallback ? h('span', { class: 'tag' }, 'fallback') : null),
						h('div', { class: 'queue-meta', title: drops.join('\n') }, shown),
					),
				);
			}),
		),
	);
}

// ---------- console ----------

function consoleLine(line) {
	const t = new Date(line.time);
	return h('div', { class: 'console-line' },
		h('time', { datetime: t.toISOString() }, t.toLocaleTimeString()),
		line.text);
}

function appendConsole(lines, reset = false) {
	const root = $('#console');
	const stick = reset || root.scrollHeight - root.scrollTop - root.clientHeight < 40;
	if (reset) root.replaceChildren();
	root.append(...lines.map(consoleLine));
	while (root.childElementCount > 300) root.firstElementChild.remove();
	if (stick) root.scrollTop = root.scrollHeight;
}

// ---------- channels ----------

function renderChannels() {
	const root = $('#channels');
	const list = state.channels || [];
	const online = list.filter((c) => c.online).length;
	$('#channels-count').textContent = list.length ? `${online} live of ${list.length}` : '';
	if (!list.length) {
		root.replaceChildren(h('p', { class: 'empty' }, 'No channels tracked yet.'));
		return;
	}
	const groups = new Map();
	for (const c of list) {
		const key = c.online ? c.game_id || 'unknown' : 'offline';
		if (!groups.has(key)) groups.set(key, { name: c.online ? c.game || 'Unknown game' : 'Offline', icon: c.online ? c.game_icon : null, items: [] });
		groups.get(key).items.push(c);
	}
	const ordered = [...groups.entries()].sort(([a], [b]) => (a === 'offline') - (b === 'offline'));
	const scroll = root.scrollTop;
	root.replaceChildren(
		...ordered.map(([, g]) => {
			const viewers = g.items.reduce((sum, c) => sum + Math.max(c.viewers, 0), 0);
			return h('section', { class: 'channel-group' },
				h('div', { class: 'channel-group-head' },
					g.icon ? thumb(g.icon, 'thumb', 30, 40) : null,
					h('div', null,
						h('div', { class: 'channel-group-name' }, g.name),
						h('div', { class: 'channel-group-meta' },
							`${g.items.length} channel${g.items.length === 1 ? '' : 's'}`,
							viewers ? ` · ${fmtNumber(viewers)} viewers` : ''),
					),
				),
				h('div', { class: 'channel-rows' }, g.items.map(channelRow)),
			);
		}),
	);
	root.scrollTop = scroll;
}

function channelRow(c) {
	const cls = ['channel'];
	if (c.watching) cls.push('watching');
	if (c.watchable) cls.push('watchable');
	if (!c.online) cls.push('offline');
	let meta;
	if (c.watching) meta = h('span', { class: 'channel-meta live' }, 'Watching');
	else if (c.online) meta = h('span', { class: 'channel-meta' }, `${fmtNumber(c.viewers)} viewers`);
	else meta = h('span', { class: 'channel-meta' }, c.pending ? 'Checking...' : 'Offline');
	return h('div', { class: cls.join(' ') },
		h('button', {
			class: 'channel-pick',
			type: 'button',
			title: c.watchable ? `Watch ${c.name}` : 'This channel can\'t progress a wanted drop right now',
			onclick: () => pickChannel(c),
		},
			h('span', { class: 'channel-name' }, c.name),
			c.acl_based ? h('span', { class: 'tag tag-pink', title: 'Listed by the campaign' }, 'campaign') : null,
			c.online && c.drops_enabled ? h('span', { class: 'tag tag-green' }, 'drops') : null,
			meta,
		),
		h('a', { class: 'channel-open', href: safeURL(c.url), target: '_blank', rel: 'noopener noreferrer', title: `Open ${c.name} on Twitch`, 'aria-label': `Open ${c.name} on Twitch` }, '↗'),
	);
}

async function pickChannel(c) {
	if (c.watching) return;
	if (!c.watchable) {
		toast(`${c.name} can't progress any wanted drop right now.`);
		return;
	}
	try {
		await api('/api/channels/select', { id: c.id });
		toast(`Switching to ${c.name}...`);
	} catch (err) {
		toast(err.message, true);
	}
}

// ---------- inventory ----------

function campaignMatches(c) {
	switch (invFilter.status) {
		case 'active': if (!c.active) return false; break;
		case 'upcoming': if (!c.upcoming) return false; break;
		case 'mining': if (c.mining_finished || c.expired) return false; break;
		case 'finished': if (!c.finished) return false; break;
	}
	if (invFilter.link === 'linked' && !c.linked) return false;
	if (invFilter.link === 'not_linked' && c.linked) return false;
	const q = invFilter.search.trim().toLowerCase();
	return !q || c.game_name.toLowerCase().includes(q) || c.name.toLowerCase().includes(q);
}

function renderInventory() {
	const list = (state.inventory || []).filter(campaignMatches);
	$('#inv-count').textContent = `${list.length} of ${(state.inventory || []).length} campaigns`;
	for (const chip of document.querySelectorAll('#inv-status .chip')) {
		chip.setAttribute('aria-pressed', String(chip.dataset.status === invFilter.status));
	}
	$('#inv-link').value = invFilter.link;
	if (document.activeElement !== $('#inv-search')) $('#inv-search').value = invFilter.search;

	const root = $('#inventory');
	if (!list.length) {
		root.replaceChildren(h('p', { class: 'empty' }, state.inventory.length ? 'No campaigns match these filters.' : 'The inventory is still loading.'));
		return;
	}
	root.replaceChildren(...list.map(campaignCard));
}

function campaignCard(c) {
	const url = safeURL(c.url);
	const linkURL = safeURL(c.link_url);
	const tags = [];
	if (c.active) tags.push(h('span', { class: 'tag tag-green' }, 'active'));
	else if (c.upcoming) tags.push(h('span', { class: 'tag' }, 'upcoming'));
	else tags.push(h('span', { class: 'tag tag-red' }, 'expired'));
	if (c.discovered) {
		tags.push(h('span', { class: 'tag tag-pink', title: 'Twitch hides the campaign list from this login, so this was found on a live channel. Its account-link status is unknown, so check on Twitch that your game account is linked.' }, 'found on stream'));
	} else if (c.linked) tags.push(h('span', { class: 'tag' }, 'linked'));
	else if (linkURL) tags.push(h('a', { href: linkURL, target: '_blank', rel: 'noopener noreferrer', class: 'tag tag-red', title: 'Link your game account to earn these rewards' }, 'not linked ↗'));
	else tags.push(h('span', { class: 'tag tag-red' }, 'not linked'));
	tags.push(h('span', { class: c.finished ? 'tag tag-green' : 'tag' }, `${c.claimed_drops}/${c.total_drops} claimed`));

	return h('article', { class: c.mining_finished && !c.finished ? 'panel campaign dim' : 'panel campaign' },
		h('div', { class: 'campaign-head' },
			thumb(c.game_icon, 'thumb', 48, 64),
			h('div', null,
				url ? h('a', { class: 'campaign-title', href: url, target: '_blank', rel: 'noopener noreferrer' }, c.name) : h('span', { class: 'campaign-title' }, c.name),
				h('div', { class: 'campaign-game' }, c.game_name),
				h('div', { class: 'campaign-tags' }, tags),
			),
		),
		h('div', { class: 'campaign-dates' },
			h('span', { class: 'muted' }, c.upcoming ? `Starts ${relative(c.starts_at)}` : `Started ${relative(c.starts_at)}`),
			h('span', { class: 'muted' }, c.expired ? `Ended ${relative(c.ends_at)}` : `Ends ${relative(c.ends_at)}`),
		),
		h('ul', { class: 'drops' }, c.drops.map(dropRow)),
	);
}

function dropRow(d) {
	const img = d.benefits.find((b) => safeURL(b.image_url));
	const bg = img ? art(img.image_url, 40, 40) : null;
	let stateText;
	let stateCls = 'drop-state';
	if (d.is_claimed) {
		stateText = 'Claimed ✓';
		stateCls += ' claimed';
	} else if (d.is_ignored) {
		stateText = d.ignored_reason === 'keyword' ? `Ignored (“${d.ignored_detail}”)` : 'Ignored (needs an ignored drop)';
	} else if (d.is_skipped) {
		stateText = 'Skipped';
	} else {
		stateText = `${d.current_minutes}/${d.required_minutes} min`;
	}
	const names = d.benefits.map((b) => b.name).join(', ') || d.name;
	return h('li', { class: 'drop' },
		h('span', { class: 'drop-img', style: bg ? { backgroundImage: bg } : null, 'aria-hidden': 'true' }),
		h('div', { class: 'drop-body' },
			h('div', { class: 'drop-line' },
				h('span', { class: 'drop-name', title: `${d.name}\n${names}` }, names),
				h('span', { class: stateCls }, stateText)),
			h('div', { class: d.is_claimed ? 'bar thin done' : 'bar thin' },
				h('span', { class: 'bar-fill', style: { width: `${Math.round(d.progress * 100)}%` } })),
		),
	);
}

$('#inv-status').addEventListener('click', (e) => {
	const chip = e.target.closest('.chip');
	if (!chip) return;
	invFilter.status = chip.dataset.status;
	storage.set('inventory-filter', invFilter);
	renderInventory();
});

$('#inv-link').addEventListener('change', (e) => {
	invFilter.link = e.target.value;
	storage.set('inventory-filter', invFilter);
	renderInventory();
});

$('#inv-search').addEventListener('input', (e) => {
	invFilter.search = e.target.value;
	storage.set('inventory-filter', invFilter);
	renderInventory();
});

// ---------- settings ----------

const BENEFITS = [
	['BADGE', 'Badges'],
	['EMOTE', 'Emotes'],
	['DIRECT_ENTITLEMENT', 'In-game items'],
	['UNKNOWN', 'Other rewards'],
];

const ALERT_EVENTS = [
	['campaign_started', 'Campaign started', 'A new campaign for a game you\'d mine'],
	['drop_claimed', 'Drop received', 'Each reward the miner claims'],
	['campaign_completed', 'Campaign completed', 'Every drop in a campaign is claimed'],
	['login_required', 'Login needed', 'Twitch logged the miner out; includes the new code'],
];

async function saveSettings(patch, message) {
	try {
		state.settings = await api('/api/settings', patch);
		if (message) toast(message);
	} catch (err) {
		if (err.data?.settings) state.settings = err.data.settings;
		toast(err.message, true);
	}
	renderSettings(true);
	renderGamesTab();
}

// renderSettings refreshes the form. Text fields being edited are left
// alone unless force is set (after the user saves).
function renderSettings(force = false) {
	const s = state.settings;
	if (!s) return;

	$('#allow-unlinked').checked = s.allow_unlinked_campaigns;

	$('#benefits').replaceChildren(
		...BENEFITS.map(([key, label]) =>
			h('label', { class: 'check' },
				h('input', {
					type: 'checkbox',
					checked: s.mining_benefits[key] !== false,
					onchange: (e) => saveSettings({ mining_benefits: { [key]: e.target.checked } }),
				}),
				label)),
	);

	const setField = (sel, value) => {
		const el = $(sel);
		if (force || (document.activeElement !== el && !el.dataset.dirty)) {
			el.value = value;
			delete el.dataset.dirty;
		}
	};
	setField('#blacklist', s.drop_name_blacklist.join('\n'));
	setField('#refresh', s.minimum_refresh_interval_minutes);
	setField('#quality', s.connection_quality);
	setField('#proxy', s.proxy);
	setField('#telegram-chat', s.telegram_chat_id);

	$('#alert-events').replaceChildren(
		...ALERT_EVENTS.map(([key, label, detail]) =>
			h('label', { class: 'check' },
				h('input', {
					type: 'checkbox',
					checked: s.alert_events?.[key] !== false,
					onchange: (e) => saveSettings({ alert_events: { [key]: e.target.checked } }),
				}),
				h('span', { class: 'check-text' }, label, h('small', null, detail)))),
	);
	const connected = s.telegram_configured || s.discord_configured;
	const hint = $('#alerts-hint');
	hint.textContent = connected
		? 'Choose what to hear about. Alerts go to every connected channel.'
		: 'Connect Telegram, Discord or both below to start receiving alerts.';
	hint.classList.toggle('warn', !connected);

	// The server never sends secrets back; it only says whether they're set.
	const alertState = (id, on) => {
		const tag = $(id);
		tag.textContent = on ? 'on' : 'off';
		tag.className = on ? 'tag tag-green' : 'tag';
	};
	alertState('#telegram-state', s.telegram_configured);
	alertState('#discord-state', s.discord_configured);
	// Blank fields keep the saved secret, so say so without taking much room.
	$('#telegram-token').placeholder = s.telegram_token_set ? 'Saved (hidden)' : '123456789:ABC-DEF...';
	$('#discord-webhook').placeholder = s.discord_configured ? 'Saved (hidden). Paste a new URL to replace it.' : 'https://discord.com/api/webhooks/...';
	$('#telegram-test').disabled = !s.telegram_configured;
	$('#telegram-clear').disabled = !s.telegram_token_set && !s.telegram_chat_id;
	$('#discord-test').disabled = !s.discord_configured;
	$('#discord-clear').disabled = !s.discord_configured;
}

for (const sel of ['#blacklist', '#refresh', '#quality', '#proxy', '#telegram-chat']) {
	$(sel).addEventListener('input', (e) => { e.target.dataset.dirty = '1'; });
}

// ---------- campaign alerts ----------

$('#telegram-save').addEventListener('click', async () => {
	const token = $('#telegram-token');
	await saveSettings({ telegram_bot_token: token.value.trim(), telegram_chat_id: $('#telegram-chat').value.trim() }, 'Telegram settings saved');
	token.value = '';
});

$('#discord-save').addEventListener('click', async () => {
	const hook = $('#discord-webhook');
	await saveSettings({ discord_webhook_url: hook.value.trim() }, 'Discord webhook saved');
	hook.value = '';
});

$('#telegram-clear').addEventListener('click', () => saveSettings({ clear_telegram: true }, 'Telegram alerts removed'));
$('#discord-clear').addEventListener('click', () => saveSettings({ clear_discord: true }, 'Discord alerts removed'));

for (const target of ['telegram', 'discord']) {
	$(`#${target}-test`).addEventListener('click', async (e) => {
		const btn = e.currentTarget;
		btn.disabled = true;
		try {
			await api('/api/alerts/test', { target });
			toast(`Test alert sent to ${target === 'telegram' ? 'Telegram' : 'Discord'}`);
		} catch (err) {
			toast(err.message, true);
		} finally {
			btn.disabled = false;
		}
	});
}

// ---------- pause ----------

function renderPaused() {
	const btn = $('#pause');
	btn.hidden = !loggedIn();
	btn.textContent = state.paused ? 'Resume mining' : 'Pause';
	btn.classList.toggle('btn-paused', !!state.paused);
}

$('#pause').addEventListener('click', () =>
	api('/api/pause', { paused: !state.paused }).catch((err) => toast(err.message, true)));

$('#allow-unlinked').addEventListener('change', (e) => saveSettings({ allow_unlinked_campaigns: e.target.checked }));

$('#blacklist-save').addEventListener('click', () => {
	const lines = $('#blacklist').value.split('\n');
	saveSettings({ drop_name_blacklist: lines }, 'Ignored drops saved');
});

$('#connection-save').addEventListener('click', () => {
	const refresh = Number($('#refresh').value);
	if (!Number.isInteger(refresh) || refresh < 5 || refresh > 1440) {
		return toast('The reload interval must be a whole number of minutes between 5 and 1440.', true);
	}
	saveSettings({
		minimum_refresh_interval_minutes: refresh,
		connection_quality: Number($('#quality').value),
		proxy: $('#proxy').value.trim(),
	}, 'Connection settings saved');
});

$('#reload').addEventListener('click', () =>
	api('/api/reload').then(() => toast('Reloading the inventory...')).catch((err) => toast(err.message, true)));

$('#logout').addEventListener('click', () =>
	confirmDialog('Log out of Twitch?', 'TwitchMiner forgets its saved login and shows a new activation code. Your settings are kept.', 'Log out', () =>
		api('/api/logout').then(() => toast('Logged out')).catch((err) => toast(err.message, true))));

// confirmDialog uses the shared modal styles from app.css.
function confirmDialog(title, text, action, onConfirm) {
	const previous = document.activeElement;
	const close = () => {
		overlay.remove();
		document.removeEventListener('keydown', onKey);
		previous?.focus?.();
	};
	const onKey = (e) => { if (e.key === 'Escape') close(); };
	const confirmBtn = h('button', { type: 'button', class: 'btn-danger', onclick: () => { close(); onConfirm(); } }, action);
	const overlay = h('div', { class: 'modal-overlay', onclick: (e) => { if (e.target === overlay) close(); } },
		h('div', { class: 'modal-content', role: 'dialog', 'aria-modal': 'true', 'aria-labelledby': 'dialog-title' },
			h('h2', { id: 'dialog-title' }, title),
			h('p', null, text),
			h('div', { class: 'modal-actions' },
				h('button', { type: 'button', class: 'btn-ghost', onclick: close }, 'Cancel'),
				confirmBtn),
		),
	);
	document.addEventListener('keydown', onKey);
	document.body.append(overlay);
	confirmBtn.focus();
}

// ---------- games tab ----------

const sameName = (a, b) => a.toLowerCase() === b.toLowerCase();
const listedGames = () => state.settings?.games_to_watch || [];
const isListed = (name) => listedGames().some((g) => sameName(g, name));
const plural = (n, word) => `${n} ${word}${n === 1 ? '' : 's'}`;

// gameCatalog summarizes every game with a campaign, most useful first:
// games with drops left to mine, then active ones, then by the earliest end.
function gameCatalog() {
	const byName = new Map();
	const entry = (name, icon) => {
		const key = name.toLowerCase();
		if (!byName.has(key)) {
			byName.set(key, { name, icon: null, active: 0, upcoming: 0, linked: false, starts: Infinity, ends: Infinity, toMine: 0, rewards: [] });
		}
		const g = byName.get(key);
		if (!g.icon && safeURL(icon)) g.icon = icon;
		return g;
	};
	for (const c of state.inventory || []) {
		if (c.expired) continue;
		const g = entry(c.game_name, c.game_icon);
		if (c.active) g.active++;
		if (c.upcoming) {
			g.upcoming++;
			g.starts = Math.min(g.starts, Date.parse(c.starts_at));
		}
		g.linked ||= c.linked;
		g.ends = Math.min(g.ends, Date.parse(c.ends_at));
		for (const d of c.drops) {
			if (d.is_claimed || !d.is_mineable) continue;
			g.toMine++;
			for (const b of d.benefits) {
				if (g.rewards.length < 3 && !g.rewards.includes(b.name)) g.rewards.push(b.name);
			}
		}
	}
	for (const name of state.games || []) entry(name);
	return [...byName.values()].sort((a, b) =>
		(b.toMine > 0) - (a.toMine > 0) || (b.active > 0) - (a.active > 0) || a.ends - b.ends || a.name.localeCompare(b.name));
}

function gameMeta(g) {
	if (!g || !(g.active || g.upcoming)) return 'No campaign right now';
	const parts = [];
	if (g.active) parts.push(plural(g.active, 'active campaign'));
	else parts.push(`Starts ${relative(new Date(g.starts).toISOString())}`);
	parts.push(g.toMine ? `${plural(g.toMine, 'drop')} to mine` : 'nothing left to mine');
	if (Number.isFinite(g.ends)) parts.push(`ends ${relative(new Date(g.ends).toISOString())}`);
	return parts.join(' · ');
}

function addGame(name) {
	name = name.trim();
	if (!name || !state.settings) return;
	if (isListed(name)) return toast(`${name} is already on your watch list.`);
	const known = gameCatalog().find((g) => sameName(g.name, name));
	if (known) name = known.name;
	setWatchList([...listedGames(), name], `Added ${name}`);
}

function removeGame(name) {
	setWatchList(listedGames().filter((g) => g !== name), `Removed ${name}`);
}

function moveGame(from, to) {
	const list = [...listedGames()];
	const [game] = list.splice(from, 1);
	list.splice(to, 0, game);
	setWatchList(list);
}

// setWatchList shows the new order right away, then saves it.
function setWatchList(list, message) {
	state.settings.games_to_watch = list;
	renderGamesTab();
	renderWanted();
	saveSettings({ games_to_watch: list }, message);
}

let drag = null;
let renderDeferred = false;

function renderGamesTab() {
	if (!state.settings) return;
	if (drag) {
		renderDeferred = true;
		return;
	}
	renderDeferred = false;
	const catalog = gameCatalog();
	const byName = new Map(catalog.map((g) => [g.name.toLowerCase(), g]));
	const list = listedGames();
	// Keep keyboard focus on the same handle when the list is redrawn.
	const focused = document.activeElement?.closest?.('#games .game-row')?.dataset.index;
	$('#priority-only').checked = state.settings.priority_list_only;
	$('#ending-soonest').checked = state.settings.priority_ending_soonest;
	$('#watch-hint').textContent = state.settings.priority_ending_soonest
		? 'Games are mined by whichever campaign ends soonest. Drag to set the order used for ties.'
		: 'Mined from top to bottom. Drag a game to change its priority.';
	$('#watch-count').textContent = list.length ? plural(list.length, 'game') : '';
	$('#games').replaceChildren(
		...(list.length
			? list.map((name, i) => watchRow(name, i, list.length, byName.get(name.toLowerCase())))
			: [h('li', { class: 'empty' }, 'Your watch list is empty. Search above or add a game from the list.')]),
	);
	if (focused !== undefined) $(`#games .game-row[data-index="${focused}"] .drag-handle`)?.focus();
	renderAvailable(catalog);
	if (combo.open) renderSuggestions();
}

function watchRow(name, i, total, g) {
	return h('li', { class: 'game-row', 'data-index': i, onpointerdown: (e) => startDrag(e, i) },
		h('button', {
			class: 'drag-handle',
			type: 'button',
			title: 'Drag to reorder',
			'aria-label': `${name}, priority ${i + 1} of ${total}. Use arrow keys to move.`,
			onkeydown: (e) => keyMove(e, i),
		}, h('span', { class: 'grip', 'aria-hidden': 'true' })),
		h('span', { class: 'rank' }, i + 1),
		thumb(g?.icon, 'thumb thumb-sm', 30, 40),
		h('div', { class: 'game-row-body' },
			h('span', { class: 'name', title: name }, name),
			h('span', { class: g?.toMine ? 'game-row-meta' : 'game-row-meta dim' }, gameMeta(g))),
		h('button', { class: 'icon-btn remove', type: 'button', title: 'Remove', 'aria-label': `Remove ${name}`, onclick: () => removeGame(name) }, '✕'),
	);
}

function keyMove(e, i) {
	const last = listedGames().length - 1;
	const to = { ArrowUp: i - 1, ArrowDown: i + 1, Home: 0, End: last }[e.key];
	if (to === undefined) return;
	e.preventDefault();
	if (to < 0 || to > last || to === i) return;
	moveGame(i, to);
	$(`#games .game-row[data-index="${to}"] .drag-handle`)?.focus();
}

// startDrag reorders with pointer events so mouse, pen and touch all work.
// Touch drags start only from the handle, so the list still scrolls.
function startDrag(e, from) {
	if (e.button !== 0 || e.target.closest('.icon-btn')) return;
	if (e.pointerType !== 'mouse' && !e.target.closest('.drag-handle')) return;
	const listEl = $('#games');
	const rows = [...listEl.querySelectorAll('.game-row')];
	if (rows.length < 2) return;
	e.preventDefault();
	const row = rows[from];
	const tops = rows.map((r) => r.getBoundingClientRect().top);
	const step = tops[1] - tops[0];
	const startY = e.clientY;
	drag = { to: from };
	row.setPointerCapture(e.pointerId);
	row.classList.add('dragging');
	listEl.classList.add('sorting');

	const move = (ev) => {
		const dy = Math.max(tops[0] - tops[from], Math.min(tops.at(-1) - tops[from], ev.clientY - startY));
		row.style.transform = `translateY(${dy}px)`;
		const to = Math.max(0, Math.min(rows.length - 1, from + Math.round(dy / step)));
		drag.to = to;
		rows.forEach((r, i) => {
			if (r === row) return;
			let shift = 0;
			if (from < to && i > from && i <= to) shift = -step;
			else if (from > to && i >= to && i < from) shift = step;
			r.style.transform = shift ? `translateY(${shift}px)` : '';
		});
	};
	const end = (ev) => {
		row.removeEventListener('pointermove', move);
		row.removeEventListener('pointerup', end);
		row.removeEventListener('pointercancel', end);
		const { to } = drag;
		drag = null;
		listEl.classList.remove('sorting');
		if (ev.type === 'pointerup' && to !== from) {
			moveGame(from, to);
			return;
		}
		row.classList.remove('dragging');
		rows.forEach((r) => { r.style.transform = ''; });
		if (renderDeferred) renderGamesTab();
	};
	row.addEventListener('pointermove', move);
	row.addEventListener('pointerup', end);
	row.addEventListener('pointercancel', end);
}

function renderAvailable(catalog) {
	const root = $('#available');
	if (!catalog.length) {
		root.replaceChildren(h('li', { class: 'empty' }, state.inventory.length ? 'No game has an open campaign right now.' : 'Campaigns are still loading.'));
		$('#games-add-all').disabled = true;
		return;
	}
	root.replaceChildren(...catalog.map((g) => {
		const listed = isListed(g.name);
		return h('li', { class: listed ? 'available-item listed' : 'available-item' },
			thumb(g.icon, 'thumb', 36, 48),
			h('div', { class: 'available-body' },
				h('div', { class: 'available-name' }, g.name, ' ',
					(g.active || g.upcoming) && !g.linked ? h('span', { class: 'tag tag-red', title: 'Link your game account on Twitch to earn these rewards' }, 'not linked') : null),
				h('div', { class: 'available-meta' }, gameMeta(g)),
				g.rewards.length ? h('div', { class: 'available-rewards', title: g.rewards.join('\n') }, g.rewards.join(' · ')) : null,
			),
			listed
				? h('span', { class: 'added' }, 'Added ✓')
				: h('button', { type: 'button', class: 'btn-ghost small-btn', 'aria-label': `Add ${g.name}`, onclick: () => addGame(g.name) }, 'Add'),
		);
	}));
	$('#games-add-all').disabled = catalog.every((g) => isListed(g.name));
}

$('#games-add-all').addEventListener('click', () => {
	const extra = gameCatalog().map((g) => g.name).filter((name) => !isListed(name));
	if (extra.length) setWatchList([...listedGames(), ...extra], `Added ${plural(extra.length, 'game')}`);
});

$('#priority-only').addEventListener('change', (e) => saveSettings({ priority_list_only: e.target.checked }));
$('#ending-soonest').addEventListener('change', (e) => saveSettings({ priority_ending_soonest: e.target.checked }));

// ---------- game search (combobox) ----------

const combo = { open: false, items: [], active: -1 };
const comboInput = $('#game-input');
const comboList = $('#game-suggestions');

// suggestionItems lists unlisted games matching the query, prefix matches
// first, plus an option to add exactly what was typed.
function suggestionItems() {
	const typed = comboInput.value.trim();
	const q = typed.toLowerCase();
	const catalog = gameCatalog();
	let matches = catalog.filter((g) => !isListed(g.name) && g.name.toLowerCase().includes(q));
	if (q) matches.sort((a, b) => b.name.toLowerCase().startsWith(q) - a.name.toLowerCase().startsWith(q));
	const items = matches.slice(0, 8).map((game) => ({ name: game.name, game }));
	if (typed && !isListed(typed) && !catalog.some((g) => sameName(g.name, typed))) items.push({ name: typed, custom: true });
	return items;
}

function renderSuggestions() {
	combo.items = suggestionItems();
	combo.active = Math.min(combo.active, combo.items.length - 1);
	if (!combo.items.length) {
		const typed = comboInput.value.trim();
		comboList.replaceChildren(h('li', { class: 'suggestion-empty', role: 'presentation' },
			typed ? `${typed} is already on your watch list.` : 'Every game with a campaign is on your list. Type a name to add another game.'));
	} else {
		comboList.replaceChildren(...combo.items.map((item, i) =>
			h('li', {
				id: `suggestion-${i}`,
				role: 'option',
				class: i === combo.active ? 'suggestion active' : 'suggestion',
				'aria-selected': String(i === combo.active),
				onpointerdown: (e) => {
					e.preventDefault();
					pickSuggestion(i);
				},
				onpointerenter: () => setActiveSuggestion(i),
			},
			item.custom ? h('span', { class: 'thumb thumb-sm suggestion-plus', 'aria-hidden': 'true' }, '+') : thumb(item.game.icon, 'thumb thumb-sm', 30, 40),
			h('div', { class: 'suggestion-body' },
				h('div', { class: 'suggestion-name' }, item.custom ? `Add “${item.name}”` : item.name),
				h('div', { class: 'suggestion-meta' }, item.custom ? 'Mining starts once Twitch shows you a campaign for it.' : gameMeta(item.game))),
			)));
	}
	updateActiveDescendant();
}

function setActiveSuggestion(i) {
	combo.active = i;
	comboList.querySelectorAll('.suggestion').forEach((el, j) => {
		el.classList.toggle('active', j === i);
		el.setAttribute('aria-selected', String(j === i));
	});
	comboList.querySelector('.suggestion.active')?.scrollIntoView({ block: 'nearest' });
	updateActiveDescendant();
}

function updateActiveDescendant() {
	if (combo.active >= 0) comboInput.setAttribute('aria-activedescendant', `suggestion-${combo.active}`);
	else comboInput.removeAttribute('aria-activedescendant');
}

function openCombo() {
	if (combo.open) return;
	combo.open = true;
	comboList.hidden = false;
	comboInput.setAttribute('aria-expanded', 'true');
	renderSuggestions();
}

function closeCombo() {
	combo.open = false;
	combo.active = -1;
	comboList.hidden = true;
	comboInput.setAttribute('aria-expanded', 'false');
	updateActiveDescendant();
}

function pickSuggestion(i) {
	const item = combo.items[i];
	if (!item) return;
	addGame(item.name);
	comboInput.value = '';
	combo.active = -1;
	renderSuggestions();
}

comboInput.addEventListener('focus', openCombo);
comboInput.addEventListener('blur', closeCombo);
comboInput.addEventListener('input', () => {
	combo.active = comboInput.value.trim() ? 0 : -1;
	openCombo();
	renderSuggestions();
});
comboInput.addEventListener('keydown', (e) => {
	switch (e.key) {
		case 'ArrowDown':
			e.preventDefault();
			openCombo();
			if (combo.items.length) setActiveSuggestion((combo.active + 1) % combo.items.length);
			break;
		case 'ArrowUp':
			e.preventDefault();
			openCombo();
			if (combo.items.length) setActiveSuggestion((combo.active - 1 + combo.items.length) % combo.items.length);
			break;
		case 'Enter':
			e.preventDefault();
			if (combo.open && combo.active >= 0) pickSuggestion(combo.active);
			else if (comboInput.value.trim()) {
				addGame(comboInput.value);
				comboInput.value = '';
				renderSuggestions();
			}
			break;
		case 'Escape':
			if (combo.open) closeCombo();
			else comboInput.value = '';
			break;
	}
});

// ---------- live updates ----------

const renderers = {
	status: renderStatus,
	login: renderLogin,
	settings: () => { renderSettings(); renderGamesTab(); renderWanted(); },
	channels: renderChannels,
	inventory: () => { renderInventory(); renderGamesTab(); },
	progress: () => { renderProgress(); renderWanted(); },
	wanted: renderWanted,
	manual: renderManual,
	games: renderGamesTab,
	paused: renderPaused,
	pubsub: renderPubsub,
	version: () => { $('#version').textContent = `TwitchMiner ${state.version}`; },
};

function connect() {
	const es = new EventSource('/api/events');
	es.onopen = () => setConnection(true);
	es.onerror = () => setConnection(false);
	for (const [name, render] of Object.entries(renderers)) {
		es.addEventListener(name, (e) => {
			state[name] = JSON.parse(e.data);
			render();
		});
	}
	es.addEventListener('console', (e) => appendConsole([JSON.parse(e.data)]));
	es.addEventListener('console_history', (e) => appendConsole(JSON.parse(e.data), true));
}

setInterval(() => {
	tickProgress();
	tickLogin();
}, 1000);

showTab(activeTab);
renderLogin();
renderProgress();
renderWanted();
renderChannels();
renderInventory();
connect();
