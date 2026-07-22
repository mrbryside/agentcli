import React, {useEffect, useRef, useState} from 'react';
import useBaseUrl from '@docusaurus/useBaseUrl';

const REDOC_SCRIPT_ID = 'embedded-redoc-script';
const REDOC_SCRIPT_URL =
  'https://cdn.redocly.com/redoc/v2.5.3/bundles/redoc.standalone.js';
const REDOC_SCRIPT_INTEGRITY =
  'sha384-xiEssMQFSpSfLbzRZCGfxxIM5QDb2DTrU6vyoZdp2sV1L6pmOMy6MpTtUoLbpC96';

const redocOptions = {
  menuToggle: true,
  nativeScrollbars: true,
  schemasExpansionLevel: 1,
  scrollYOffset: () =>
    document.querySelector('.navbar')?.getBoundingClientRect().height ?? 0,
  showExtensions: true,
  sortOperationsAlphabetically: true,
};

function loadRedoc() {
  if (window.Redoc) {
    return Promise.resolve(window.Redoc);
  }

  return new Promise((resolve, reject) => {
    const existingScript = document.getElementById(REDOC_SCRIPT_ID);
    const script = existingScript ?? document.createElement('script');

    script.addEventListener('load', () => resolve(window.Redoc), {once: true});
    script.addEventListener(
      'error',
      () => reject(new Error('Unable to load the Redoc renderer.')),
      {once: true},
    );

    if (!existingScript) {
      script.id = REDOC_SCRIPT_ID;
      script.src = REDOC_SCRIPT_URL;
      script.integrity = REDOC_SCRIPT_INTEGRITY;
      script.crossOrigin = 'anonymous';
      document.head.appendChild(script);
    }
  });
}

export default function EmbeddedRedoc() {
  const containerRef = useRef(null);
  const [error, setError] = useState('');
  const [loading, setLoading] = useState(true);
  const specificationUrl = useBaseUrl('/openapi/swagger.json');

  useEffect(() => {
    let active = true;
    setError('');
    setLoading(true);

    loadRedoc()
      .then((redoc) => {
        if (!active || !containerRef.current) {
          return;
        }

        redoc.init(
          specificationUrl,
          redocOptions,
          containerRef.current,
          (initError) => {
            if (!active) {
              return;
            }
            if (initError) {
              setError(
                initError instanceof Error ? initError.message : String(initError),
              );
              return;
            }
            setLoading(false);
          },
        );
      })
      .catch((value) => {
        if (active) {
          setError(value instanceof Error ? value.message : String(value));
        }
      });

    return () => {
      active = false;
      if (containerRef.current) {
        containerRef.current.replaceChildren();
      }
    };
  }, [specificationUrl]);

  if (error) {
    return <div className="alert alert--danger">{error}</div>;
  }

  return (
    <div className="redoclyEmbeddedReference">
      {loading && (
        <div className="redoclyLoading">Loading API documentation…</div>
      )}
      <div className="redoclyEmbeddedContent" ref={containerRef} />
    </div>
  );
}
