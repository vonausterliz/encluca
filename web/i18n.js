// Traduzione delle pagine (incluso al posto del segnaposto I18N da serveUI).
// Il testo di riferimento è l'italiano; ogni pagina definisce il dizionario EN (italiano -> inglese).
// - testi dinamici: t('testo con {n}', {n: 3})
// - testi statici, placeholder, aria-label, title: tradotti percorrendo il DOM; l'originale
//   italiano resta memorizzato sul nodo, così si può tornare indietro.
let LANG = 'it';
try { if (localStorage.getItem('encluca-lang') === 'en') LANG = 'en'; } catch (e) {}
const LOCALE = () => (LANG === 'en' ? 'en-GB' : 'it-IT');

function t(key, vars) {
  let s = (LANG === 'en' && EN[key]) || key;
  if (vars) for (const k in vars) s = s.split('{' + k + '}').join(vars[k]);
  return s;
}

function translateDom(root) {
  const walker = document.createTreeWalker(root, NodeFilter.SHOW_TEXT, {
    acceptNode(n) {
      const p = n.parentElement;
      return !p || p.closest('script,style,[data-noi18n]') ? NodeFilter.FILTER_REJECT : NodeFilter.FILTER_ACCEPT;
    },
  });
  for (let n; (n = walker.nextNode()); ) {
    if (n._it === undefined) {
      const k = n.nodeValue.trim();
      if (!k || !(k in EN)) continue;
      n._it = n.nodeValue;
    }
    const k = n._it.trim();
    n.nodeValue = LANG === 'en' ? n._it.replace(k, EN[k]) : n._it;
  }
  for (const el of root.querySelectorAll('[placeholder],[aria-label],[title]')) {
    for (const a of ['placeholder', 'aria-label', 'title']) {
      if (!el.hasAttribute(a)) continue;
      const key = 'it' + a.replace('-', '');
      if (el.dataset[key] === undefined) {
        const v = el.getAttribute(a);
        if (!(v in EN)) continue;
        el.dataset[key] = v;
      }
      const orig = el.dataset[key];
      el.setAttribute(a, LANG === 'en' ? EN[orig] : orig);
    }
  }
}

let titleIt = null;
function setLangUI(l) {
  LANG = l === 'en' ? 'en' : 'it';
  try { localStorage.setItem('encluca-lang', LANG); } catch (e) {}
  document.documentElement.lang = LANG;
  if (titleIt === null) titleIt = document.title;
  document.title = t(titleIt);
  const sel = document.getElementById('langSel');
  if (sel) sel.value = LANG;
  translateDom(document.body);
  if (typeof onLangChange === 'function') onLangChange();
}
document.addEventListener('DOMContentLoaded', () => setLangUI(LANG));
