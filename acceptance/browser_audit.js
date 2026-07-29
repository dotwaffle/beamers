(surface) => {
  const visible = (element) => {
    const style = getComputedStyle(element);
    const bounds = element.getBoundingClientRect();
    return style.display !== "none" &&
      style.visibility !== "hidden" &&
      bounds.width > 0 &&
      bounds.height > 0;
  };
  const identify = (element) => {
    let found = element.localName;
    if (element.id) {
      found += `#${element.id}`;
    }
    if (element.getAttribute("name")) {
      found += `[name="${element.getAttribute("name")}"]`;
    }
    return found;
  };
  const controls = [...document.querySelectorAll(
    "a[href],button,input,select,textarea,summary",
  )].filter((element) => !element.matches('input[type="hidden"]') &&
    visible(element));
  const unlabeledControls = controls
    .filter((element) => {
      if (element.localName === "button" ||
        element.localName === "a" ||
        element.localName === "summary") {
        return !element.textContent.trim() &&
          !element.getAttribute("aria-label") &&
          !element.getAttribute("aria-labelledby");
      }
      return element.labels.length === 0 &&
        !element.getAttribute("aria-label") &&
        !element.getAttribute("aria-labelledby");
    })
    .map(identify);
  const smallTargets = controls
    .filter((element) => {
      const target = element.matches('input[type="checkbox"],input[type="radio"]')
        ? element.labels[0] || element
        : element;
      const bounds = target.getBoundingClientRect();
      return bounds.width < 24 || bounds.height < 24;
    })
    .map(identify);
  const parseColor = (value) => {
    const match = value.match(
      /^rgba?\(\s*([\d.]+)[,\s]+([\d.]+)[,\s]+([\d.]+)(?:\s*[,/]\s*([\d.]+))?\s*\)$/,
    );
    if (!match) {
      return null;
    }
    return {
      red: Number(match[1]),
      green: Number(match[2]),
      blue: Number(match[3]),
      alpha: match[4] === undefined ? 1 : Number(match[4]),
    };
  };
  const opaqueBackground = (element) => {
    for (let current = element; current; current = current.parentElement) {
      const found = parseColor(getComputedStyle(current).backgroundColor);
      if (found && found.alpha === 1) {
        return found;
      }
    }
    return {red: 255, green: 255, blue: 255, alpha: 1};
  };
  const luminance = (color) => {
    const channel = (value) => {
      const normalized = value / 255;
      return normalized <= 0.04045
        ? normalized / 12.92
        : ((normalized + 0.055) / 1.055) ** 2.4;
    };
    return 0.2126 * channel(color.red) +
      0.7152 * channel(color.green) +
      0.0722 * channel(color.blue);
  };
  const contrast = (foreground, background) => {
    const light = Math.max(luminance(foreground), luminance(background));
    const dark = Math.min(luminance(foreground), luminance(background));
    return (light + 0.05) / (dark + 0.05);
  };
  const contrastFailures = [...document.querySelectorAll(
    "h1,h2,h3,p,label,button,a,li,strong,input,select,summary",
  )]
    .filter((element) => visible(element) && (
      element.textContent.trim() || element.value
    ))
    .flatMap((element) => {
      const style = getComputedStyle(element);
      const foreground = parseColor(style.color);
      if (!foreground || foreground.alpha !== 1) {
        return [identify(element)];
      }
      const fontSize = Number.parseFloat(style.fontSize);
      const fontWeight = Number.parseInt(style.fontWeight, 10) || 400;
      const minimum = fontSize >= 24 || (fontSize >= 18.66 && fontWeight >= 700)
        ? 3
        : 4.5;
      const ratio = contrast(foreground, opaqueBackground(element));
      return ratio + 0.01 < minimum
        ? [`${identify(element)} (${ratio.toFixed(2)}:1)`]
        : [];
    });
  const active = document.activeElement;
  const keyboardOperable = active &&
    active !== document.body &&
    active.matches("a[href],button,input,select,textarea,summary,[tabindex]");
  const activeStyle = keyboardOperable ? getComputedStyle(active) : null;
  const focusVisible = Boolean(activeStyle) && (
    activeStyle.outlineStyle !== "none" &&
      Number.parseFloat(activeStyle.outlineWidth) > 0 ||
    activeStyle.boxShadow !== "none"
  );
  const durationMilliseconds = (value) => value.endsWith("ms")
    ? Number.parseFloat(value)
    : Number.parseFloat(value) * 1000;
  const motionDurations = [...document.querySelectorAll("*")]
    .filter(visible)
    .flatMap((element) => {
      const style = getComputedStyle(element);
      return [
        ...style.animationDuration.split(","),
        ...style.transitionDuration.split(","),
      ].map((value) => durationMilliseconds(value.trim()));
    });
  const skip = document.querySelector('a.skip-link[href^="#"]');
  const main = skip && document.querySelector(skip.getAttribute("href"));
  const headings = [...document.querySelectorAll("h1,h2,h3,h4,h5,h6")]
    .filter((heading) => !heading.closest("nav"));
  const levels = headings.map(
    (heading) => Number(heading.localName.slice(1)),
  );
  const hierarchy = levels.filter((level) => level === 1).length === 1 &&
    levels.every(
      (level, index) => index === 0 || level <= levels[index - 1] + 1,
    );
  const tables = [...document.querySelectorAll("table")].every((table) =>
    Boolean(table.querySelector("caption")?.textContent.trim()) &&
      [...table.querySelectorAll("th")].every((heading) =>
        ["col", "row"].includes(heading.getAttribute("scope"))
      )
  );
  const selections = [...document.querySelectorAll(
    'input[type="checkbox"],input[type="radio"]',
  )].every((control) => {
    const labelBounds = control.labels[0]?.getBoundingClientRect();
    return labelBounds?.width >= 24 && labelBounds?.height >= 24;
  });
  const status = document.querySelector("#display-connection");
  return {
    surface,
    title: document.title.trim(),
    language: document.documentElement.lang,
    main: Boolean(document.querySelector("main")),
    heading: Boolean(document.querySelector("h1")),
    structure: Boolean(skip && main?.matches("main[tabindex='-1']")) &&
      hierarchy && tables && selections,
    keyboard_operable: Boolean(keyboardOperable),
    focus_visible: focusVisible,
    reduced_motion: matchMedia("(prefers-reduced-motion: reduce)").matches &&
      motionDurations.every((duration) => duration <= 0.01),
    non_color_status: Boolean(
      status &&
      status.textContent.trim() &&
      status.dataset.connection,
    ),
    unlabeled_controls: unlabeledControls,
    small_targets: smallTargets,
    contrast_failures: contrastFailures,
  };
}
