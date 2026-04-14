/**
 * AI Response Filter — iOS 26 Safari
 *
 * Hides user messages and UI chrome on major AI chat platforms,
 * leaving only the assistant's responses visible.
 *
 * Usage options:
 *   1. iOS Shortcut  → "Run JavaScript on Web Page" action, paste this script.
 *   2. Bookmarklet   → Minify and prefix with  javascript:
 *
 * Supported platforms:
 *   • ChatGPT       (chatgpt.com / chat.openai.com)
 *   • Claude        (claude.ai)
 *   • Gemini        (gemini.google.com)
 *   • Perplexity    (perplexity.ai)
 *   • Copilot       (copilot.microsoft.com)
 *   • DeepSeek      (chat.deepseek.com)
 *   • Meta AI       (meta.ai)
 *   • Generic       (falls back to role/aria heuristics)
 */

(function () {
  "use strict";

  /* ─── Constants ─────────────────────────────────────────── */
  const TOGGLE_ID   = "__ai_filter_toggle__";
  const STYLE_ID    = "__ai_filter_style__";
  const HIDDEN_CLS  = "__ai_filter_hidden__";
  const ACTIVE_KEY  = "__ai_filter_active__";

  /* ─── Platform detection ────────────────────────────────── */
  const host = location.hostname;

  const PLATFORMS = {
    chatgpt: {
      match: /chatgpt\.com|chat\.openai\.com/,
      userMsg:    '[data-message-author-role="user"]',
      assistantMsg: '[data-message-author-role="assistant"]',
      hideExtra: [
        "nav",
        "header",
        ".sticky",
        "[data-testid='send-button']",
        "form",
      ],
    },
    claude: {
      match: /claude\.ai/,
      userMsg:    '[data-testid="human-turn"], .human-turn',
      assistantMsg: '[data-testid="ai-turn"], .ai-turn',
      hideExtra: [
        "nav",
        "header",
        ".sidebar",
        "[class*='InputArea']",
        "[class*='BottomBar']",
        "fieldset",
      ],
    },
    gemini: {
      match: /gemini\.google\.com/,
      userMsg:    ".user-query-container, .user-prompt-container",
      assistantMsg: "model-response, .model-response-text",
      hideExtra: [
        "side-navigation-v2",
        ".input-area-container",
        "chat-input",
        "run-button-tooltip-wrapper",
      ],
    },
    perplexity: {
      match: /perplexity\.ai/,
      userMsg:    "[class*='UserMessage'], [class*='userMessage']",
      assistantMsg: "[class*='AnswerBody'], [class*='answerBody'], [class*='prose']",
      hideExtra: [
        "nav",
        "[class*='FollowUpInput']",
        "[class*='SearchInput']",
        "[class*='Sidebar']",
      ],
    },
    copilot: {
      match: /copilot\.microsoft\.com/,
      userMsg:    "[class*='userMessage'], [data-author='user']",
      assistantMsg: "[class*='botMessage'], [data-author='bot']",
      hideExtra: [
        "header",
        "nav",
        "[class*='InputBar']",
        "[class*='Footer']",
      ],
    },
    deepseek: {
      match: /chat\.deepseek\.com/,
      userMsg:    "[class*='fbb737a4']",   // user bubble class (may change with updates)
      assistantMsg: "[class*='ds-markdown'], [class*='ac15e2c5']",
      hideExtra: [
        "aside",
        "header",
        "[class*='inputPanel']",
        "[class*='chatInput']",
      ],
    },
    metaai: {
      match: /meta\.ai/,
      userMsg:    "[class*='UserMessage'], [aria-label='Your message']",
      assistantMsg: "[class*='AssistantMessage'], [class*='BotMessage']",
      hideExtra: [
        "nav",
        "header",
        "[class*='InputRow']",
        "[class*='Footer']",
      ],
    },
  };

  /* ─── Resolve platform config ───────────────────────────── */
  function getPlatform() {
    for (const key of Object.keys(PLATFORMS)) {
      if (PLATFORMS[key].match.test(host)) return PLATFORMS[key];
    }
    return null; // will use generic fallback
  }

  /* ─── Generic heuristic selectors ──────────────────────────
   *  Works on any chat UI that uses ARIA roles or common
   *  data attributes to label turns.
   */
  const GENERIC = {
    userSelectors: [
      '[data-role="user"]',
      '[data-author="user"]',
      '[aria-label*="You"]',
      '[class*="UserMessage"]',
      '[class*="userMessage"]',
      '[class*="human"]',
      '[class*="Human"]',
    ],
    assistantSelectors: [
      '[data-role="assistant"]',
      '[data-author="assistant"]',
      '[data-author="bot"]',
      '[aria-label*="AI"]',
      '[class*="AssistantMessage"]',
      '[class*="BotMessage"]',
      '[class*="assistant"]',
      '[class*="ai-response"]',
    ],
  };

  /* ─── Inject CSS ────────────────────────────────────────── */
  function injectStyles() {
    if (document.getElementById(STYLE_ID)) return;
    const style = document.createElement("style");
    style.id = STYLE_ID;
    style.textContent = `
      .${HIDDEN_CLS} {
        display: none !important;
        visibility: hidden !important;
      }

      /* Toggle button — floating pill */
      #${TOGGLE_ID} {
        position: fixed;
        bottom: env(safe-area-inset-bottom, 24px);
        right: 16px;
        z-index: 2147483647;
        padding: 10px 18px;
        border-radius: 999px;
        border: none;
        cursor: pointer;
        font: 600 14px/1 -apple-system, 'SF Pro Text', sans-serif;
        background: #0a84ff;
        color: #fff;
        box-shadow: 0 4px 16px rgba(0,0,0,.35);
        -webkit-tap-highlight-color: transparent;
        transition: opacity .2s, transform .15s;
        letter-spacing: .3px;
      }
      #${TOGGLE_ID}:active {
        opacity: .75;
        transform: scale(.95);
      }
      #${TOGGLE_ID}.off {
        background: #636366;
      }
    `;
    document.head.appendChild(style);
  }

  /* ─── Collect elements to hide ──────────────────────────── */
  function collectUserElements(platform) {
    const els = new Set();

    if (platform) {
      // Hide exact user-turn containers
      document.querySelectorAll(platform.userMsg).forEach((el) => {
        // Walk up to find the outermost turn wrapper (max 4 levels)
        let node = el;
        for (let i = 0; i < 4; i++) {
          const p = node.parentElement;
          if (!p || p === document.body) break;
          // Stop if the parent also contains assistant nodes
          if (p.querySelector(platform.assistantMsg)) break;
          node = p;
        }
        els.add(node);
      });

      // Hide extra chrome
      platform.hideExtra.forEach((sel) => {
        document.querySelectorAll(sel).forEach((el) => els.add(el));
      });
    } else {
      // Generic: hide everything that looks like user input
      GENERIC.userSelectors.forEach((sel) => {
        document.querySelectorAll(sel).forEach((el) => els.add(el));
      });
    }

    return els;
  }

  /* ─── Apply / remove filter ─────────────────────────────── */
  function applyFilter(platform) {
    const targets = collectUserElements(platform);
    targets.forEach((el) => el.classList.add(HIDDEN_CLS));

    // Expand assistant messages in case they are collapsed
    if (platform) {
      document.querySelectorAll(platform.assistantMsg).forEach((el) => {
        el.style.removeProperty("max-height");
        el.style.removeProperty("overflow");
      });
    }
  }

  function removeFilter() {
    document.querySelectorAll(`.${HIDDEN_CLS}`).forEach((el) =>
      el.classList.remove(HIDDEN_CLS)
    );
  }

  /* ─── Toggle button ─────────────────────────────────────── */
  function createToggle(platform) {
    if (document.getElementById(TOGGLE_ID)) return;

    const btn = document.createElement("button");
    btn.id = TOGGLE_ID;
    btn.textContent = "AI Only: ON";

    btn.addEventListener("click", () => {
      const isActive = sessionStorage.getItem(ACTIVE_KEY) !== "false";
      if (isActive) {
        removeFilter();
        btn.textContent = "AI Only: OFF";
        btn.classList.add("off");
        sessionStorage.setItem(ACTIVE_KEY, "false");
      } else {
        applyFilter(platform);
        btn.textContent = "AI Only: ON";
        btn.classList.remove("off");
        sessionStorage.setItem(ACTIVE_KEY, "true");
      }
    });

    document.body.appendChild(btn);
  }

  /* ─── Mutation observer — handle dynamically added turns ── */
  function watchForNewTurns(platform) {
    const observer = new MutationObserver(() => {
      if (sessionStorage.getItem(ACTIVE_KEY) === "false") return;
      applyFilter(platform);
    });

    observer.observe(document.body, {
      childList: true,
      subtree: true,
    });
  }

  /* ─── Entry point ───────────────────────────────────────── */
  function run() {
    const platform = getPlatform();

    injectStyles();
    sessionStorage.setItem(ACTIVE_KEY, "true");
    applyFilter(platform);
    createToggle(platform);
    watchForNewTurns(platform);
  }

  // Run immediately if DOM is ready, otherwise wait
  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", run);
  } else {
    run();
  }
})();
